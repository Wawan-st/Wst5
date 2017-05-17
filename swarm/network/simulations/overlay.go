// +build none

// You can run this simulation using
//
//    go run ./swarm/network/simulations/overlay.go
package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/swarm/network"
)

type Simulation struct {
	mtx    sync.Mutex
	stores map[discover.NodeID]*adapters.stateStore
}

func NewSimulation() *Simulation {
	return &Simulation{
		stores: make(map[discover.NodeID]*adapters.stateStore),
	}
}

func (s *Simulation) NewService(id *adapters.NodeId, snapshot []byte) node.Service {
	s.mtx.Lock()
	store, ok := s.stores[id.NodeID]
	if !ok {
		store = NewSimStore()
		s.stores[id.NodeID] = store
	}
	s.mtx.Unlock()

	addr := network.NewAddrFromNodeId(id)

	kp := network.NewKadParams()
	kp.MinProxBinSize = 2
	kp.MaxBinSize = 8
	kp.MinBinSize = 2
	kp.MaxRetries = 1000
	kp.RetryExponent = 2
	kp.RetryInterval = 1000
	kad := network.NewKademlia(addr.Over(), kp)

	hp := network.NewHiveParams()
	hp.KeepAliveInterval = 3 * time.Second

	config := &network.BzzConfig{
		OverlayAddr:  addr.Over(),
		UnderlayAddr: addr.Under(),
		HiveParams:   hp,
	}

	return network.NewBzz(config, kad, store)
}

func createMockers() map[string]*simulations.MockerConfig {
	configs := make(map[string]*simulations.MockerConfig)

	defaultCfg := simulations.DefaultMockerConfig()
	defaultCfg.Id = "start-stop"
	defaultCfg.Description = "Starts and Stops nodes in go routines"
	defaultCfg.Mocker = startStopMocker

	bootNetworkCfg := simulations.DefaultMockerConfig()
	bootNetworkCfg.Id = "bootNet"
	bootNetworkCfg.Description = "Only boots up all nodes in the config"
	bootNetworkCfg.Mocker = bootMocker

	randomNodesCfg := simulations.DefaultMockerConfig()
	randomNodesCfg.Id = "randomNodes"
	randomNodesCfg.Description = "Boots nodes and then starts and stops some picking randomly"
	randomNodesCfg.Mocker = randomMocker

	configs[defaultCfg.Id] = defaultCfg
	configs[bootNetworkCfg.Id] = bootNetworkCfg
	configs[randomNodesCfg.Id] = randomNodesCfg

	return configs
}

func setupMocker(net *simulations.Network) []*adapters.NodeId {
	conf := net.Config()
	conf.DefaultService = "overlay"

	nodeCount := 50
	ids := make([]*adapters.NodeId, nodeCount)
	for i := 0; i < nodeCount; i++ {
		node, err := net.NewNode()
		if err != nil {
			panic(err.Error())
		}
		ids[i] = node.ID()
	}

	for _, id := range ids {
		if err := net.Start(id); err != nil {
			panic(err.Error())
		}
		log.Debug(fmt.Sprintf("node %v starting up", id))
	}
	for i, id := range ids {
		var peerId *adapters.NodeId
		if i == 0 {
			peerId = ids[len(ids)-1]
		} else {
			peerId = ids[i-1]
		}
		ch := make(chan network.OverlayAddr)
		go func() {
			defer close(ch)
			ch <- network.NewAddrFromNodeId(peerId)
		}()
		if err := net.GetNode(id).Node.(*adapters.SimNode).Service().(*network.Bzz).Hive.Register(ch); err != nil {
			panic(err.Error())
		}
	}

	return ids
}

func bootMocker(net *simulations.Network) {
	setupMocker(net)
}

func randomMocker(net *simulations.Network) {
	ids := setupMocker(net)

	for {
		var lowid, highid int
		randWait := rand.Intn(5000) + 1000
		rand1 := rand.Intn(9)
		rand2 := rand.Intn(9)
		if rand1 < rand2 {
			lowid = rand1
			highid = rand2
		} else if rand1 > rand2 {
			highid = rand1
			lowid = rand2
		} else {
			if rand1 == 0 {
				rand2 = 9
			} else if rand1 == 9 {
				rand1 = 0
			}
		}
		for i := lowid; i < highid; i++ {
			log.Debug(fmt.Sprintf("node %v shutting down", ids[i]))
			net.Stop(ids[i])
			go func(id *adapters.NodeId) {
				time.Sleep(time.Duration(randWait) * time.Millisecond)
				net.Start(id)
			}(ids[i])
			time.Sleep(time.Duration(randWait) * time.Millisecond)
		}
	}
}

func startStopMocker(net *simulations.Network) {
	ids := setupMocker(net)

	for range time.Tick(10 * time.Second) {
		id := ids[rand.Intn(len(ids))]
		go func() {
			log.Error("stopping node", "id", id)
			if err := net.Stop(id); err != nil {
				log.Error("error stopping node", "id", id, "err", err)
				return
			}

			time.Sleep(3 * time.Second)

			log.Error("starting node", "id", id)
			if err := net.Start(id); err != nil {
				log.Error("error starting node", "id", id, "err", err)
				return
			}
		}()
	}
}

// var server
func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	log.Root().SetHandler(log.LvlFilterHandler(log.LvlDebug, log.StreamHandler(os.Stderr, log.TerminalFormat(false))))

	s := NewSimulation()
	services := adapters.Services{
		"overlay": s.NewService,
	}
	adapters.RegisterServices(services)

	mockers := createMockers()

	config := &simulations.ServerConfig{
		NewAdapter:      func() adapters.NodeAdapter { return adapters.NewSimAdapter(services) },
		DefaultMockerId: "bootNet",
		Mockers:         mockers,
	}

	log.Info("starting simulation server on 0.0.0.0:8888...")
	http.ListenAndServe(":8888", simulations.NewServer(config))
}

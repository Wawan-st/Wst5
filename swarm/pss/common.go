package pss

import (
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/protocols"
	"github.com/ethereum/go-ethereum/swarm/network"
	"github.com/ethereum/go-ethereum/swarm/storage"
)	

type pssPingMsg struct {
	Created time.Time
}

type pssPing struct {
	quitC chan struct{}
}

func (self *pssPing) pssPingHandler(msg interface{}) error {
	log.Warn("got ping", "msg", msg)
	self.quitC <- struct{}{}
	return nil
}

var pssPingProtocol = &protocols.Spec{
	Name:       "psstest",
	Version:    1,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		pssPingMsg{},
	},
}

var pssPingTopic = NewTopic(pssPingProtocol.Name, int(pssPingProtocol.Version))

func newTestPss(addr []byte) *Pss {	
	if addr == nil {
		addr = network.RandomAddr().OAddr
	}
	
	// set up storage
	cachedir, err := ioutil.TempDir("", "pss-cache")
	if err != nil {
		log.Error("create pss cache tmpdir failed", "error", err)
		os.Exit(1)
	}
	dpa, err := storage.NewLocalDPA(cachedir)
	if err != nil {
		log.Error("local dpa creation failed", "error", err)
		os.Exit(1)
	}
	
	// set up routing
	kp := network.NewKadParams()
	kp.MinProxBinSize = 3

	// create pss
	pp := NewPssParams()

	overlay := network.NewKademlia(addr, kp)
	ps := NewPss(overlay, dpa, pp)

	return ps
}

func newPssPingMsg(ps *Pss, to []byte, spec *protocols.Spec, topic PssTopic, senderaddr []byte) PssMsg {
	data := pssPingMsg{
		Created: time.Now(),
	}
	code, found := spec.GetCode(&data)
	if !found {
		return PssMsg{}
	}

	rlpbundle, err := newProtocolMsg(code, data)
	if err != nil {
		return PssMsg{}
	}

	pssmsg := PssMsg{
		To: to,
		Payload: NewPssEnvelope(senderaddr, topic, rlpbundle),
	}

	return pssmsg
}

func newPssPingProtocol(handler func (interface{}) error) *p2p.Protocol {
	return &p2p.Protocol{
		Name: pssPingProtocol.Name,
		Version: pssPingProtocol.Version,
		Length: uint64(pssPingProtocol.MaxMsgSize),
		Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
			pp := protocols.NewPeer(p, rw, pssPingProtocol)
			log.Trace(fmt.Sprintf("running pss vprotocol on peer %v", p))
			err := pp.Run(handler)
			return err
		},
	}
}

type testPssPeer struct {
	*protocols.Peer
	addr []byte
}

func (self *testPssPeer) Address() []byte {
	return self.addr
}

func (self *testPssPeer) Off() network.OverlayAddr {
	return self
}

func (self *testPssPeer) Drop(err error) {
}

func (self *testPssPeer) Update(o network.OverlayAddr) network.OverlayAddr {
	return self
}

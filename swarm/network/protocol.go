// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package network

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/protocols"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	NetworkId          = 322 // BZZ in l33t
	ProtocolMaxMsgSize = 10 * 1024 * 1024
)

var BzzProtocol = &protocols.Spec{
	Name:       "bzz",
	Version:    1,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		bzzHandshake{},
	},
}

var DiscoveryProtocol = &protocols.Spec{
	Name:       "hive",
	Version:    1,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		peersMsg{},
		getPeersMsg{},
		subPeersMsg{},
	},
}

var PssProtocol = &protocols.Spec{
	Name:       "pss",
	Version:    1,
	MaxMsgSize: 10 * 1024 * 1024,
	Messages: []interface{}{
		PssMsg{},
	},
}

// the Addr interface that peerPool needs
type Addr interface {
	OverlayPeer
	Over() []byte
	Under() []byte
	String() string
}

// Peer interface represents an live peer connection
type Peer interface {
	Addr                   // the address of a peer
	Conn                   // the live connection (protocols.Peer)
	LastActive() time.Time // last time active
}

// Conn interface represents an live peer connection
type Conn interface {
	ID() discover.NodeID                                         // the key that uniquely identifies the Node for the peerPool
	Handshake(context.Context, interface{}) (interface{}, error) // can send messages
	Send(interface{}) error                                      // can send messages
	Drop(error)                                                  // disconnect this peer
	Run(func(interface{}) error) error                           // the run function to run a protocol
}

// TODO: implement store for exec nodes
type Store interface {
	Load(string) ([]byte, error)
	Save(string, []byte) error
}

type BzzConfig struct {
	OverlayAddr  []byte
	UnderlayAddr []byte

	KadParams  *KadParams
	HiveParams *HiveParams
	PssParams  *PssParams

	Store Store
}

func NewBzz(config *BzzConfig) *Bzz {
	kademlia := NewKademlia(config.OverlayAddr, config.KadParams)
	bzz := &Bzz{
		Kademlia:   kademlia,
		Hive:       NewHive(config.HiveParams, kademlia, config.Store),
		localAddr:  &bzzAddr{config.OverlayAddr, config.UnderlayAddr},
		handshakes: make(map[discover.NodeID]*bzzHandshake),
	}
	if config.PssParams != nil {
		bzz.Pss = NewPss(kademlia, config.PssParams)
	}
	return bzz
}

type Bzz struct {
	Kademlia *Kademlia
	Hive     *Hive
	Pss      *Pss

	localAddr  *bzzAddr
	mtx        sync.Mutex
	handshakes map[discover.NodeID]*bzzHandshake
}

func (b *Bzz) Protocols() []p2p.Protocol {
	return []p2p.Protocol{
		{
			Name:    BzzProtocol.Name,
			Version: BzzProtocol.Version,
			Length:  BzzProtocol.Length(),
			Run:     b.runHandshake,
		},
		{
			Name:     DiscoveryProtocol.Name,
			Version:  DiscoveryProtocol.Version,
			Length:   DiscoveryProtocol.Length(),
			Run:      b.runProtocol(DiscoveryProtocol, b.Hive.Run),
			NodeInfo: b.Hive.NodeInfo,
			PeerInfo: b.Hive.PeerInfo,
		},
		{
			Name:    PssProtocol.Name,
			Version: PssProtocol.Version,
			Length:  PssProtocol.Length(),
			Run:     b.runProtocol(PssProtocol, b.Pss.Run),
		},
	}
}

func (b *Bzz) APIs() []rpc.API {
	return []rpc.API{{
		Namespace: "hive",
		Version:   "1.0",
		Service:   b.Hive,
	}}
}

func (b *Bzz) Start(server *p2p.Server) error {
	return b.Hive.Start(server)
}

func (b *Bzz) Stop() error {
	b.Hive.Stop()
	return nil
}

func (b *Bzz) runHandshake(p *p2p.Peer, rw p2p.MsgReadWriter) error {
	handshake := b.getHandshake(p.ID())

	if err := handshake.Perform(p, rw); err != nil {
		log.Error("handshake failed", "peer", p.ID(), "err", err)
		return err
	}

	// fail if we get another handshake
	msg, err := rw.ReadMsg()
	if err != nil {
		return err
	}
	msg.Discard()
	return errors.New("received multiple handshakes")
}

func (b *Bzz) runProtocol(spec *protocols.Spec, run func(*bzzPeer) error) func(*p2p.Peer, p2p.MsgReadWriter) error {
	return func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
		// wait for the bzz protocol to perform the handshake
		handshake := b.getHandshake(p.ID())
		if err := handshake.Wait(); err != nil {
			return err
		}

		// the handshake has succeeded so run the service
		peer := &bzzPeer{
			Conn:      protocols.NewPeer(p, rw, spec),
			localAddr: b.localAddr,
			bzzAddr:   handshake.peerAddr,
		}
		return run(peer)
	}
}

func (b *Bzz) getHandshake(peerID discover.NodeID) *bzzHandshake {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	handshake, ok := b.handshakes[peerID]
	if !ok {
		handshake = &bzzHandshake{
			Version:   uint64(BzzProtocol.Version),
			NetworkId: uint64(NetworkId),
			Addr:      b.localAddr,
			done:      make(chan struct{}),
		}
		b.handshakes[peerID] = handshake
	}
	return handshake
}

// bzzPeer is the bzz protocol view of a protocols.Peer (itself an extension of p2p.Peer)
// implements the Peer interface and all interfaces Peer implements: Addr, OverlayPeer
type bzzPeer struct {
	Conn                 // represents the connection for online peers
	localAddr  *bzzAddr  // local Peers address
	*bzzAddr             // remote address -> implements Addr interface = protocols.Peer
	lastActive time.Time // time is updated whenever mutexes are releasing
}

func newBzzPeer(conn Conn, over, under []byte) *bzzPeer {
	return &bzzPeer{
		Conn:      conn,
		localAddr: &bzzAddr{over, under},
	}
}

// Off returns the overlay peer record for offline persistance
func (self *bzzPeer) Off() OverlayAddr {
	return self.bzzAddr
}

// LastActive returns the time the peer was last active
func (self *bzzPeer) LastActive() time.Time {
	return self.lastActive
}

/*
 Handshake

* Version: 8 byte integer version of the protocol
* NetworkID: 8 byte integer network identifier
* Addr: the address advertised by the node including underlay and overlay connecctions
*/
type bzzHandshake struct {
	Version   uint64
	NetworkId uint64
	Addr      *bzzAddr

	// peerAddr is the address received in the peer handshake
	peerAddr *bzzAddr

	done chan struct{}
	err  error
}

func (self *bzzHandshake) String() string {
	return fmt.Sprintf("Handshake: Version: %v, NetworkId: %v, Addr: %v", self.Version, self.NetworkId, self.Addr)
}

const bzzHandshakeTimeout = time.Second

func (self *bzzHandshake) Perform(p *p2p.Peer, rw p2p.MsgReadWriter) (err error) {
	defer func() {
		self.err = err
		close(self.done)
	}()
	peer := protocols.NewPeer(p, rw, BzzProtocol)
	ctx, cancel := context.WithTimeout(context.Background(), bzzHandshakeTimeout)
	defer cancel()
	hs, err := peer.Handshake(ctx, self)
	if err != nil {
		return err
	}
	rhs := hs.(*bzzHandshake)
	if rhs.NetworkId != self.NetworkId {
		return fmt.Errorf("network id mismatch %d (!= %d)", rhs.NetworkId, self.NetworkId)
	}
	if rhs.Version != self.Version {
		return fmt.Errorf("version mismatch %d (!= %d)", rhs.Version, self.Version)
	}
	self.peerAddr = rhs.Addr
	return nil
}

func (self *bzzHandshake) Wait() error {
	select {
	case <-self.done:
		return self.err
	case <-time.After(bzzHandshakeTimeout):
		return errors.New("timed out waiting for bzz handshake")
	}
}

// bzzAddr implements the PeerAddr interface
type bzzAddr struct {
	OAddr []byte
	UAddr []byte
}

// implements OverlayPeer interface to be used in pot package
func (self *bzzAddr) Address() []byte {
	return self.OAddr
}

func (self *bzzAddr) Bytes() []byte {
	return self.OAddr
}
func (self *bzzAddr) Over() []byte {
	return self.OAddr
}

func (self *bzzAddr) Under() []byte {
	return self.UAddr
}

func (self *bzzAddr) On(p OverlayConn) OverlayConn {
	bp := p.(*bzzPeer)
	bp.bzzAddr = self
	return bp
}

func (self *bzzAddr) Update(a OverlayAddr) OverlayAddr {
	return &bzzAddr{self.OAddr, a.(Addr).Under()}
}

func (self *bzzAddr) String() string {
	return fmt.Sprintf("%x <%x>", self.OAddr, self.UAddr)
}

// RandomAddr is a utility method generating an address from a public key
func RandomAddr() *bzzAddr {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic("unable to generate key")
	}
	pubkey := crypto.FromECDSAPub(&key.PublicKey)
	var id discover.NodeID
	copy(id[:], pubkey[1:])
	return &bzzAddr{
		OAddr: crypto.Keccak256(pubkey[1:]),
		UAddr: id[:],
	}
}

// NewNodeIdFromAddr transforms the underlay address to an adapters.NodeId
func NewNodeIdFromAddr(addr Addr) *adapters.NodeId {
	return adapters.NewNodeId(addr.Under())
}

// NewAddrFromNodeId constucts a bzzAddr from an adapters.NodeId
// the overlay address is derived as the hash of the nodeId
func NewAddrFromNodeId(n *adapters.NodeId) *bzzAddr {
	id := n.NodeID
	return &bzzAddr{crypto.Keccak256(id[:]), id[:]}
}

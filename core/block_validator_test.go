// Copyright 2015 The go-ethereum Authors
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

package core

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/expanse-org/go-expanse/common"
	"github.com/expanse-org/go-expanse/core/state"
	"github.com/expanse-org/go-expanse/core/types"
	"github.com/expanse-org/go-expanse/core/vm"
	"github.com/expanse-org/go-expanse/ethdb"
	"github.com/expanse-org/go-expanse/event"
	"github.com/expanse-org/go-expanse/params"
)

func testChainConfig() *params.ChainConfig {
	return params.TestChainConfig
	//return &params.ChainConfig{HomesteadBlock: big.NewInt(0)}
}

func proc() (Validator, *BlockChain) {
	db, _ := ethdb.NewMemDatabase()
	var mux event.TypeMux

	WriteTestNetGenesisBlock(db)
	blockchain, err := NewBlockChain(db, testChainConfig(), thePow(), &mux, vm.Config{})
	if err != nil {
		fmt.Println(err)
	}
	return blockchain.validator, blockchain
}

func TestNumber(t *testing.T) {
	_, chain := proc()

	statedb, _ := state.New(chain.Genesis().Root(), chain.chainDb)
	cfg := testChainConfig()
	header := makeHeader(cfg, chain.Genesis(), statedb)
	header.Number = big.NewInt(3)
	err := ValidateHeader(cfg, FakePow{}, header, chain.Genesis().Header(), false, false)
	if err != BlockNumberErr {
		t.Errorf("expected block number error, got %q", err)
	}

	header = makeHeader(cfg, chain.Genesis(), statedb)
	err = ValidateHeader(cfg, FakePow{}, header, chain.Genesis().Header(), false, false)
	if err == BlockNumberErr {
		t.Errorf("didn't expect block number error")
	}
}

func TestPutReceipt(t *testing.T) {
	db, _ := ethdb.NewMemDatabase()

	var addr common.Address
	addr[0] = 1
	var hash common.Hash
	hash[0] = 2

	receipt := new(types.Receipt)
	receipt.Logs = []*types.Log{{
		Address:     addr,
		Topics:      []common.Hash{hash},
		Data:        []byte("hi"),
		BlockNumber: 42,
		TxHash:      hash,
		TxIndex:     0,
		BlockHash:   hash,
		Index:       0,
	}}

	WriteReceipts(db, types.Receipts{receipt})
	receipt = GetReceipt(db, common.Hash{})
	if receipt == nil {
		t.Error("expected to get 1 receipt, got none.")
	}
}

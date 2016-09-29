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

package vm

import (
	gmath "math"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/logger"
	"github.com/ethereum/go-ethereum/logger/glog"
	"github.com/ethereum/go-ethereum/params"
	"github.com/hashicorp/golang-lru"
)

// progStatus is the type for the JIT program status.
type progStatus int32

const (
	progUnknown progStatus = iota // unknown status
	progCompile                   // compile status
	progReady                     // ready for use status
	progError                     // error status (usually caused during compilation)

	defaultJitMaxCache int = 1024 // maximum amount of jit cached programs
)

var MaxProgSize int // Max cache size for JIT programs

var programs *lru.Cache // lru cache for the JIT programs.

func init() {
	SetJITCacheSize(defaultJitMaxCache)
}

// SetJITCacheSize recreates the program cache with the max given size. Setting
// a new cache is **not** thread safe. Use with caution.
func SetJITCacheSize(size int) {
	programs, _ = lru.New(size)
}

// GetProgram returns the program by id or nil when non-existent
func GetProgram(id common.Hash) *Program {
	if p, ok := programs.Get(id); ok {
		return p.(*Program)
	}

	return nil
}

// GenProgramStatus returns the status of the given program id
func GetProgramStatus(id common.Hash) progStatus {
	program := GetProgram(id)
	if program != nil {
		return progStatus(atomic.LoadInt32(&program.status))
	}

	return progUnknown
}

// Program is a compiled program for the JIT VM and holds all required for
// running a compiled JIT program.
type Program struct {
	Id     common.Hash // Id of the program
	status int32       // status should be accessed atomically

	contract *Contract

	instructions []programInstruction // instruction set
	mapping      map[uint64]uint64    // real PC mapping to array indices
	destinations map[uint64]struct{}  // cached jump destinations

	code []byte
}

// NewProgram returns a new JIT program
func NewProgram(code []byte) *Program {
	program := &Program{
		Id:           crypto.Keccak256Hash(code),
		mapping:      make(map[uint64]uint64),
		destinations: make(map[uint64]struct{}),
		code:         code,
	}

	programs.Add(program.Id, program)
	return program
}

func (p *Program) addInstr(op OpCode, pc uint64, fn instrFn, data *big.Int) {
	// PUSH and DUP are a bit special. They all cost the same but we do want to have checking on stack push limit
	// PUSH is also allowed to calculate the same price for all PUSHes
	// DUP requirements are handled elsewhere (except for the stack limit check)
	baseOp := op
	if op >= PUSH1 && op <= PUSH32 {
		baseOp = PUSH1
	}
	if op >= DUP1 && op <= DUP16 {
		baseOp = DUP1
	}
	base := _baseCheck[baseOp]

	returns := op == RETURN || op == SUICIDE || op == STOP
	instr := instruction{op, pc, fn, data, base.gas, base.stackPop, base.stackPush, returns}

	p.instructions = append(p.instructions, instr)
	p.mapping[pc] = uint64(len(p.instructions) - 1)
}

// CompileProgram compiles the given program and return an error when it fails
func CompileProgram(program *Program) {
	if progStatus(atomic.LoadInt32(&program.status)) == progCompile {
		return
	}
	atomic.StoreInt32(&program.status, int32(progCompile))
	defer func() {
		atomic.StoreInt32(&program.status, int32(progReady))
	}()
	if glog.V(logger.Debug) {
		glog.Infof("compiling %x\n", program.Id[:4])
		tstart := time.Now()
		defer func() {
			glog.Infof("compiled  %x instrc: %d time: %v\n", program.Id[:4], len(program.instructions), time.Since(tstart))
		}()
	}

	// loop thru the opcodes and "compile" in to instructions
	for pc := uint64(0); pc < uint64(len(program.code)); pc++ {
		switch op := OpCode(program.code[pc]); op {
		case ADD:
			program.addInstr(op, pc, opAdd, nil)
		case SUB:
			program.addInstr(op, pc, opSub, nil)
		case MUL:
			program.addInstr(op, pc, opMul, nil)
		case DIV:
			program.addInstr(op, pc, opDiv, nil)
		case SDIV:
			program.addInstr(op, pc, opSdiv, nil)
		case MOD:
			program.addInstr(op, pc, opMod, nil)
		case SMOD:
			program.addInstr(op, pc, opSmod, nil)
		case EXP:
			program.addInstr(op, pc, opExp, nil)
		case SIGNEXTEND:
			program.addInstr(op, pc, opSignExtend, nil)
		case NOT:
			program.addInstr(op, pc, opNot, nil)
		case LT:
			program.addInstr(op, pc, opLt, nil)
		case GT:
			program.addInstr(op, pc, opGt, nil)
		case SLT:
			program.addInstr(op, pc, opSlt, nil)
		case SGT:
			program.addInstr(op, pc, opSgt, nil)
		case EQ:
			program.addInstr(op, pc, opEq, nil)
		case ISZERO:
			program.addInstr(op, pc, opIszero, nil)
		case AND:
			program.addInstr(op, pc, opAnd, nil)
		case OR:
			program.addInstr(op, pc, opOr, nil)
		case XOR:
			program.addInstr(op, pc, opXor, nil)
		case BYTE:
			program.addInstr(op, pc, opByte, nil)
		case ADDMOD:
			program.addInstr(op, pc, opAddmod, nil)
		case MULMOD:
			program.addInstr(op, pc, opMulmod, nil)
		case SHA3:
			program.addInstr(op, pc, opSha3, nil)
		case ADDRESS:
			program.addInstr(op, pc, opAddress, nil)
		case BALANCE:
			program.addInstr(op, pc, opBalance, nil)
		case ORIGIN:
			program.addInstr(op, pc, opOrigin, nil)
		case CALLER:
			program.addInstr(op, pc, opCaller, nil)
		case CALLVALUE:
			program.addInstr(op, pc, opCallValue, nil)
		case CALLDATALOAD:
			program.addInstr(op, pc, opCalldataLoad, nil)
		case CALLDATASIZE:
			program.addInstr(op, pc, opCalldataSize, nil)
		case CALLDATACOPY:
			program.addInstr(op, pc, opCalldataCopy, nil)
		case CODESIZE:
			program.addInstr(op, pc, opCodeSize, nil)
		case EXTCODESIZE:
			program.addInstr(op, pc, opExtCodeSize, nil)
		case CODECOPY:
			program.addInstr(op, pc, opCodeCopy, nil)
		case EXTCODECOPY:
			program.addInstr(op, pc, opExtCodeCopy, nil)
		case GASPRICE:
			program.addInstr(op, pc, opGasprice, nil)
		case BLOCKHASH:
			program.addInstr(op, pc, opBlockhash, nil)
		case COINBASE:
			program.addInstr(op, pc, opCoinbase, nil)
		case TIMESTAMP:
			program.addInstr(op, pc, opTimestamp, nil)
		case NUMBER:
			program.addInstr(op, pc, opNumber, nil)
		case DIFFICULTY:
			program.addInstr(op, pc, opDifficulty, nil)
		case GASLIMIT:
			program.addInstr(op, pc, opGasLimit, nil)
		case PUSH1, PUSH2, PUSH3, PUSH4, PUSH5, PUSH6, PUSH7, PUSH8, PUSH9, PUSH10, PUSH11, PUSH12, PUSH13, PUSH14, PUSH15, PUSH16, PUSH17, PUSH18, PUSH19, PUSH20, PUSH21, PUSH22, PUSH23, PUSH24, PUSH25, PUSH26, PUSH27, PUSH28, PUSH29, PUSH30, PUSH31, PUSH32:
			size := uint64(op - PUSH1 + 1)
			bytes := getData([]byte(program.code), new(big.Int).SetUint64(pc+1), new(big.Int).SetUint64(size))

			program.addInstr(op, pc, opPush, common.Bytes2Big(bytes))

			pc += size

		case POP:
			program.addInstr(op, pc, opPop, nil)
		case DUP1, DUP2, DUP3, DUP4, DUP5, DUP6, DUP7, DUP8, DUP9, DUP10, DUP11, DUP12, DUP13, DUP14, DUP15, DUP16:
			program.addInstr(op, pc, opDup, big.NewInt(int64(op-DUP1+1)))
		case SWAP1, SWAP2, SWAP3, SWAP4, SWAP5, SWAP6, SWAP7, SWAP8, SWAP9, SWAP10, SWAP11, SWAP12, SWAP13, SWAP14, SWAP15, SWAP16:
			program.addInstr(op, pc, opSwap, big.NewInt(int64(op-SWAP1+2)))
		case LOG0, LOG1, LOG2, LOG3, LOG4:
			program.addInstr(op, pc, opLog, big.NewInt(int64(op-LOG0)))
		case MLOAD:
			program.addInstr(op, pc, opMload, nil)
		case MSTORE:
			program.addInstr(op, pc, opMstore, nil)
		case MSTORE8:
			program.addInstr(op, pc, opMstore8, nil)
		case SLOAD:
			program.addInstr(op, pc, opSload, nil)
		case SSTORE:
			program.addInstr(op, pc, opSstore, nil)
		case JUMP:
			program.addInstr(op, pc, opJump, nil)
		case JUMPI:
			program.addInstr(op, pc, opJumpi, nil)
		case JUMPDEST:
			program.addInstr(op, pc, opJumpdest, nil)
			program.destinations[pc] = struct{}{}
		case PC:
			program.addInstr(op, pc, opPc, big.NewInt(int64(pc)))
		case MSIZE:
			program.addInstr(op, pc, opMsize, nil)
		case GAS:
			program.addInstr(op, pc, opGas, nil)
		case CREATE:
			program.addInstr(op, pc, opCreate, nil)
		case DELEGATECALL:
			// Instruction added regardless of homestead phase.
			// Homestead (and execution of the opcode) is checked during
			// runtime.
			program.addInstr(op, pc, opDelegateCall, nil)
		case CALL:
			program.addInstr(op, pc, opCall, nil)
		case CALLCODE:
			program.addInstr(op, pc, opCallCode, nil)
		case RETURN:
			program.addInstr(op, pc, opReturn, nil)
		case SUICIDE:
			program.addInstr(op, pc, opSuicide, nil)
		case STOP: // Stop the contract
			program.addInstr(op, pc, opStop, nil)
		default:
			program.addInstr(op, pc, nil, nil)
		}
	}
}

func RunProgram(program *Program, env *Environment, contract *Contract, input []byte) ([]byte, error) {
	return New(env, Config{}).Run(contract, input)
}

// validDest checks if the given destination is a valid one given the
// destination table of the program
func validDest(dests map[uint64]struct{}, dest *big.Int) bool {
	// PC cannot go beyond len(code) and certainly can't be bigger than 64bits.
	// Don't bother checking for JUMPDEST in that case.
	if dest.Cmp(bigMaxUint64) > 0 {
		return false
	}
	_, ok := dests[dest.Uint64()]
	return ok
}

// calculateGasAndSize calculates the required given the opcode and stack items calculates the new memorysize for
// the operation. This does not reduce gas or resizes the memory.
func calculateGasAndSize(gasTable params.GasTable, env *Environment, contract *Contract, instr instruction, statedb Database, mem *Memory, stack *Stack) (uint64, uint64, error) {
	var (
		newMemSize, memGas uint64
		sizeFault          bool
	)

	gas, err := baseCalc(instr, stack)
	if err != nil {
		return 0, 0, err
	}

	// stack Check, memory resize & gas phase
	switch op := instr.op; op {
	case SUICIDE:
		// if suicide is not nil: homestead gas fork
		if gasTable.CreateBySuicide > 0 {
			gas += gasTable.Suicide
			var (
				address = common.BigToAddress(stack.data[len(stack.data)-1])
				eip158  = env.ChainConfig().IsEIP158(env.BlockNumber)
			)

			switch {
			case eip158:
				var (
					empty          = env.Db().Empty(address) // checking exist avoids going through the trie on nonexistent
					transfersValue = statedb.GetBalance(contract.Address()).BitLen() > 0
				)
				if empty && transfersValue {
					gas += gasTable.CreateBySuicide
				}
			default:
				exist := env.Db().Exist(address)
				if !exist {
					gas += gasTable.CreateBySuicide
				}
			}
		}

		if !statedb.HasSuicided(contract.Address()) {
			statedb.AddRefund(params.SuicideRefundGas)
		}
	case EXTCODESIZE:
		gas = gasTable.ExtcodeSize
	case BALANCE:
		gas = gasTable.Balance
	case SLOAD:
		gas = gasTable.SLoad
	case SWAP1, SWAP2, SWAP3, SWAP4, SWAP5, SWAP6, SWAP7, SWAP8, SWAP9, SWAP10, SWAP11, SWAP12, SWAP13, SWAP14, SWAP15, SWAP16:
		n := int(op - SWAP1 + 2)
		err := stack.require(n)
		if err != nil {
			return 0, 0, err
		}
		gas = GasFastestStep64
	case DUP1, DUP2, DUP3, DUP4, DUP5, DUP6, DUP7, DUP8, DUP9, DUP10, DUP11, DUP12, DUP13, DUP14, DUP15, DUP16:
		n := int(op - DUP1 + 1)
		err := stack.require(n)
		if err != nil {
			return 0, 0, err
		}
		gas = GasFastestStep64
	case LOG0, LOG1, LOG2, LOG3, LOG4:
		n := int(op - LOG0)
		err := stack.require(n + 2)
		if err != nil {
			return 0, 0, err
		}

		mSize, mStart := stack.data[stack.len()-2], stack.data[stack.len()-1]
		if mSize.BitLen() > 64 {
			return 0, 0, OutOfGasError
		}
		msize64 := mSize.Uint64()

		gas = (gas + LogGas64) + (uint64(n) * LogTopicGas64)
		if !math.IsMulSafe(msize64, LogDataGas64) {
			return 0, 0, OutOfGasError
		}
		gasLogData := msize64 * LogDataGas64

		if !math.IsAddSafe(gas, gasLogData) {
			return 0, 0, OutOfGasError
		}
		gas += gasLogData

		newMemSize, sizeFault = calcMemSize(mStart, mSize)
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case EXP:
		x := uint64(len(stack.data[stack.len()-2].Bytes()))
		if !math.IsMulSafe(x, ExpByteGas64) {
			return 0, 0, OutOfGasError
		}
		x *= ExpByteGas64
		if !math.IsAddSafe(gas, x) {
			return 0, 0, OutOfGasError
		}
		gas += x
	case SSTORE:
		err := stack.require(2)
		if err != nil {
			return 0, 0, err
		}

		y, x := stack.data[stack.len()-2], stack.data[stack.len()-1]
		val := statedb.GetState(contract.Address(), common.BigToHash(x))

		// This checks for 3 scenario's and calculates gas accordingly
		// 1. From a zero-value address to a non-zero value         (NEW VALUE)
		// 2. From a non-zero value address to a zero-value address (DELETE)
		// 3. From a non-zero to a non-zero                         (CHANGE)
		if common.EmptyHash(val) && !common.EmptyHash(common.BigToHash(y)) {
			gas = SstoreSetGas64
		} else if !common.EmptyHash(val) && common.EmptyHash(common.BigToHash(y)) {
			statedb.AddRefund(params.SstoreRefundGas)

			gas = SstoreClearGas64
		} else {
			gas = SstoreResetGas64
		}
	case MLOAD:
		newMemSize, sizeFault = calcMemSize(stack.peek(), u256(32))
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case MSTORE8:
		newMemSize, sizeFault = calcMemSize(stack.peek(), u256(1))
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case MSTORE:
		newMemSize, sizeFault = calcMemSize(stack.peek(), u256(32))
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case RETURN:
		newMemSize, sizeFault = calcMemSize(stack.peek(), stack.data[stack.len()-2])
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case SHA3:
		newMemSize, sizeFault = calcMemSize(stack.peek(), stack.data[stack.len()-2])
		if sizeFault {
			return 0, 0, OutOfGasError
		}

		if stack.data[stack.len()-2].BitLen() > 64 {
			return 0, 0, OutOfGasError
		}
		words := toWordSize(stack.data[stack.len()-2].Uint64())
		if !math.IsMulSafe(words, KeccakWordGas64) {
			return 0, 0, OutOfGasError
		}
		wordsGas := words * KeccakWordGas64
		if !math.IsAddSafe(gas, wordsGas) {
			return 0, 0, OutOfGasError
		}
		gas += wordsGas
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case CALLDATACOPY:
		newMemSize, sizeFault = calcMemSize(stack.peek(), stack.data[stack.len()-3])
		if sizeFault {
			return 0, 0, OutOfGasError
		}

		words := toWordSize(stack.data[stack.len()-3].Uint64())
		if !math.IsMulSafe(words, CopyGas64) {
			return 0, 0, OutOfGasError
		}
		wordsGas := words * CopyGas64
		if !math.IsAddSafe(gas, wordsGas) {
			return 0, 0, OutOfGasError
		}
		gas += wordsGas
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case CODECOPY:
		newMemSize, sizeFault = calcMemSize(stack.peek(), stack.data[stack.len()-3])
		if sizeFault {
			return 0, 0, OutOfGasError
		}

		words := toWordSize(stack.data[stack.len()-3].Uint64())
		if !math.IsMulSafe(words, CopyGas64) {
			return 0, 0, OutOfGasError
		}
		wordsGas := words * CopyGas64
		if !math.IsAddSafe(gas, wordsGas) {
			return 0, 0, OutOfGasError
		}
		gas += wordsGas
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case EXTCODECOPY:
		newMemSize, sizeFault = calcMemSize(stack.data[stack.len()-2], stack.data[stack.len()-4])
		if sizeFault {
			return 0, 0, OutOfGasError
		}

		words := toWordSize(stack.data[stack.len()-4].Uint64())
		if !math.IsMulSafe(words, CopyGas64) {
			return 0, 0, OutOfGasError
		}
		wordsGas := words * CopyGas64
		if !math.IsAddSafe(gas, wordsGas) {
			return 0, 0, OutOfGasError
		}
		gas += wordsGas
		memGas, _ = calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
	case CREATE:
		newMemSize, sizeFault = calcMemSize(stack.data[stack.len()-2], stack.data[stack.len()-3])
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas
	case CALL, CALLCODE:
		gas = gasTable.Calls

		transfersValue := stack.data[len(stack.data)-3].BitLen() > 0
		if op == CALL {
			var (
				address = common.BigToAddress(stack.data[len(stack.data)-2])
				eip158  = env.ChainConfig().IsEIP158(env.BlockNumber)
			)

			switch {
			case eip158:
				empty := env.Db().Empty(address)
				if empty && transfersValue {
					if !math.IsAddSafe(gas, CallNewAccountGas64) {
						return 0, 0, OutOfGasError
					}
					gas += CallNewAccountGas64
				}
			default:
				exist := env.Db().Exist(address)
				if !exist {
					if !math.IsAddSafe(gas, CallNewAccountGas64) {
						return 0, 0, OutOfGasError
					}
					gas += CallNewAccountGas64
				}
			}
		}

		if transfersValue {
			if !math.IsAddSafe(gas, CallValueTransferGas64) {
				return 0, 0, OutOfGasError
			}
			gas += CallValueTransferGas64
		}

		x, xSizeFault := calcMemSize(stack.data[stack.len()-6], stack.data[stack.len()-7])
		if xSizeFault {
			return 0, 0, OutOfGasError
		}
		y, ySizeFault := calcMemSize(stack.data[stack.len()-4], stack.data[stack.len()-5])
		if ySizeFault {
			return 0, 0, OutOfGasError
		}

		newMemSize = x
		if y > newMemSize {
			newMemSize = y
		}
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas

		cg := callGas(gasTable, contract.gas64, gas, stack.data[stack.len()-1].Uint64())
		// Replace the stack item with the new gas calculation. This means that
		// either the original item is left on the stack or the item is replaced by:
		// (availableGas - gas) * 63 / 64
		// We replace the stack item so that it's available when the opCall instruction is
		// called. This information is otherwise lost due to the dependency on *current*
		// available gas.
		stack.data[stack.len()-1] = new(big.Int).SetUint64(cg)
		if !math.IsAddSafe(gas, cg) {
			return 0, 0, OutOfGasError
		}
		gas += cg
	case DELEGATECALL:
		gas = gasTable.Calls

		x, xSizeFault := calcMemSize(stack.data[stack.len()-5], stack.data[stack.len()-6])
		if xSizeFault {
			return 0, 0, OutOfGasError
		}
		y, ySizeFault := calcMemSize(stack.data[stack.len()-3], stack.data[stack.len()-4])
		if ySizeFault {
			return 0, 0, OutOfGasError
		}

		newMemSize = uint64(gmath.Max(float64(x), float64(y)))
		memGas, _ := calcQuadMemGas(mem, newMemSize)
		if !math.IsAddSafe(gas, memGas) {
			return 0, 0, OutOfGasError
		}
		gas += memGas

		cg := callGas(gasTable, contract.gas64, gas, stack.data[stack.len()-1].Uint64())
		// Replace the stack item with the new gas calculation. This means that
		// either the original item is left on the stack or the item is replaced by:
		// (availableGas - gas) * 63 / 64
		// We replace the stack item so that it's available when the opCall instruction is
		// called. This information is otherwise lost due to the dependency on *current*
		// available gas.
		stack.data[stack.len()-1] = new(big.Int).SetUint64(cg)
		if !math.IsAddSafe(gas, cg) {
			return 0, 0, OutOfGasError
		}
		gas += cg
	}
	if sizeFault {
		return 0, 0, OutOfGasError
	}
	return toWordSize(newMemSize) * 32, gas, nil
}

// waitCompile returns a new channel to broadcast the new result after
// a compilation has started.
func WaitCompile(id common.Hash) chan progStatus {
	ch := make(chan progStatus)
	go func() {
		defer close(ch)
		for GetProgramStatus(id) == progCompile {
			time.Sleep(time.Microsecond * 10)
		}
		ch <- GetProgramStatus(id)
	}()
	return ch
}

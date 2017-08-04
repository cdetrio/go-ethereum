// Copyright 2014 The go-ethereum Authors
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
	"fmt"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// Config are the configuration options for the Interpreter
type Config struct {
	// Debug enabled debugging Interpreter options
	Debug bool
	// EnableJit enabled the JIT VM
	EnableJit bool
	// ForceJit forces the JIT VM
	ForceJit bool
	// Tracer is the op code logger
	Tracer Tracer
	// NoRecursion disabled Interpreter call, callcode,
	// delegate call and create.
	NoRecursion bool
	// Disable gas metering
	DisableGasMetering bool
	// Enable recording of SHA3/keccak preimages
	EnablePreimageRecording bool
	// JumpTable contains the EVM instruction table. This
	// may me left uninitialised and will be set the default
	// table.
	JumpTable [256]operation
}

// Interpreter is used to run Ethereum based contracts and will utilise the
// passed evmironment to query external sources for state information.
// The Interpreter will run the byte code VM or JIT VM based on the passed
// configuration.
type Interpreter struct {
	evm      *EVM
	cfg      Config
	gasTable params.GasTable
	intPool  *intPool

	readonly bool
}

// NewInterpreter returns a new instance of the Interpreter.
func NewInterpreter(evm *EVM, cfg Config) *Interpreter {
	// We use the STOP instruction whether to see
	// the jump table was initialised. If it was not
	// we'll set the default jump table.
	if !cfg.JumpTable[STOP].valid {
		switch {
		case evm.ChainConfig().IsHomestead(evm.BlockNumber):
			cfg.JumpTable = homesteadInstructionSet
		default:
			cfg.JumpTable = frontierInstructionSet
		}
	}

	return &Interpreter{
		evm:      evm,
		cfg:      cfg,
		gasTable: evm.ChainConfig().GasTable(evm.BlockNumber),
		intPool:  newIntPool(),
	}
}

func (in *Interpreter) enforceRestrictions(op OpCode, operation operation, stack *Stack) error {
	return nil
}

// Run loops and evaluates the contract's code with the given input data and returns
// the return byte-slice and an error if one occurred.
//
// It's important to note that any errors returned by the interpreter should be
// considered a revert-and-consume-all-gas operation. No error specific checks
// should be handled to reduce complexity and errors further down the in.
func (in *Interpreter) Run(snapshot int, contract *Contract, input []byte) (ret []byte, err error) {
	//fmt.Println("interpreter.go Run")
	in.evm.depth++
	defer func() { in.evm.depth-- }()

	// Don't bother with the execution if there's no code.
	if len(contract.Code) == 0 {
		return nil, nil
	}

	codehash := contract.CodeHash // codehash is used when doing jump dest caching
	if codehash == (common.Hash{}) {
		codehash = crypto.Keccak256Hash(contract.Code)
	}

	var (
		op    OpCode        // current opcode
		mem   = NewMemory() // bound memory
		stack = newstack()  // local stack
		stackCopy = newstack()
		logged = bool(false)
		// For optimisation reason we're using uint64 as the program counter.
		// It's theoretically possible to go above 2^64. The YP defines the PC
		// to be uint256. Practically much less so feasible.
		pc   = uint64(0) // program counter
		pcCopy = uint64(0)
		gasCopy = uint64(0)
		cost uint64
	)
	contract.Input = input

	defer func() {
		if err != nil && !logged && in.cfg.Debug {
		//if in.cfg.Debug {
			//fmt.Println("defer func interpreter.go calling Tracer.CaptureState.")
			in.cfg.Tracer.CaptureState(in.evm, pcCopy, op, gasCopy, cost, mem, stackCopy, contract, in.evm.depth, err)
			//fmt.Println("defer func interpreter.go called Tracer.CaptureState.")
			//stackCopy = newstack()
		}
	}()

	// The Interpreter main run loop (contextual). This loop runs until either an
	// explicit STOP, RETURN or SELFDESTRUCT is executed, an error occurred during
	// the execution of one of the operations or until the done flag is set by the
	// parent context.
	for atomic.LoadInt32(&in.evm.abort) == 0 {
		//fmt.Println("interpreter.go operation loop begin. stack:")
		//stack.Print()
		// Get the memory location of pc
		op = contract.GetOp(pc)
		
		logged = false

		//fmt.Println("interpreter loop begin. pc:", pc)

		// get the operation from the jump table matching the opcode
		operation := in.cfg.JumpTable[op]
		if err := in.enforceRestrictions(op, operation, stack); err != nil {
			return nil, err
		}

		//fmt.Println("interpreter.go got op from jump table. checking if valid")

		if in.cfg.Debug {
			//fmt.Println("interpreter.go clearing stackCopy.")
			pcCopy = uint64(pc)
			gasCopy = uint64(contract.Gas)

			stackCopy = newstack()
			for _, val := range stack.data {
				stackCopy.push(val)
			}
			//fmt.Println("stackCopy.Print():")
			//stackCopy.Print()
		}
		


		// if the op is invalid abort the process and return an error
		if !operation.valid {
			return nil, fmt.Errorf("invalid opcode 0x%x", int(op))
		}
		
		//fmt.Println("interpreter.go op is valid. now validating stack...")

		// validate the stack and make sure there enough stack items available
		// to perform the operation
		if err := operation.validateStack(stack); err != nil {
			return nil, err
		}
		
		//fmt.Println("interpreter.go stack validated. now checking memory...")

		var memorySize uint64
		// calculate the new memory size and expand the memory to fit
		// the operation
		if operation.memorySize != nil {
			memSize, overflow := bigUint64(operation.memorySize(stack))
			if overflow {
				return nil, errGasUintOverflow
			}
			// memory is expanded in words of 32 bytes. Gas
			// is also calculated in words.
			if memorySize, overflow = math.SafeMul(toWordSize(memSize), 32); overflow {
				return nil, errGasUintOverflow
			}
		}
		
		//fmt.Println("interpreter.go memory fits.")
		

		if !in.cfg.DisableGasMetering {
			// consume the gas and return an error if not enough gas is available.
			// cost is explicitly set so that the capture state defer method cas get the proper cost
			cost, err = operation.gasCost(in.gasTable, in.evm, contract, stack, mem, memorySize)
			if err != nil || !contract.UseGas(cost) {
				return nil, ErrOutOfGas
			}
		}
		if memorySize > 0 {
			mem.Resize(memorySize)
		}
		

		if err == nil && in.cfg.Debug {
			// trace needs to be called before operation.execute for CALLs etc
			//fmt.Println("interpreter.go calling Tracer.CaptureState before operation.execute.")
			in.cfg.Tracer.CaptureState(in.evm, pcCopy, op, gasCopy, cost, mem, stackCopy, contract, in.evm.depth, err)
			//fmt.Println("interpreter.go called Tracer.CaptureState before operation.execute.")
			logged = true
		}

		// execute the operation
		//fmt.Println("interpreter.go calling operation.execute.")
		res, err := operation.execute(&pc, in.evm, contract, mem, stack)
		//fmt.Println("interpreter.go called operation.execute.")
		


		// verifyPool is a build flag. Pool verification makes sure the integrity
		// of the integer pool by comparing values to a default value.
		if verifyPool {
			verifyIntegerPool(in.intPool)
		}

		switch {
		case err != nil:
			return nil, err
		case operation.halts:
			return res, nil
		case !operation.jumps:
			pc++
		}
		// if the operation returned a value make sure that is also set
		// the last return data.
		if res != nil {
			mem.lastReturn = ret
		}
	}
	return nil, nil
}

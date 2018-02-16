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

package ethdb

// Code using batches should try to add this much data to the batch.
// The value was determined empirically.
const IdealBatchSize = 100 * 1024

// Putter wraps the database write operation supported by both batches and regular databases.
type Putter interface {
	Put(bucket, key, value []byte) error
	PutS(bucket, key, suffix, value []byte) error
	DeleteSuffix(suffix []byte) error
}

type WalkerFunc = func(key, value []byte) []byte

type Getter interface {
	Get(bucket, key []byte) ([]byte, error)
	First(bucket, key, suffix []byte) ([]byte, error)
	Has(bucket, key []byte) (bool, error)
	Walk(bucket, key []byte, keybits uint, walker WalkerFunc) error
}

type Deleter interface {
	Delete(bucket, key[]byte) error
}

type GetterPutter interface {
	Getter
	Putter
}

// Database wraps all database operations. All methods are safe for concurrent use.
type Database interface {
	Getter
	Putter
	Delete(bucket, key []byte) error
	MultiPut(tuples ...[]byte) error
	Close()
	NewBatch() Mutation
	Size() int
}

// Extended version of the Batch, with read capabilites
type Mutation interface {
	Database
	Commit() error
	Rollback()
	Keys() [][]byte
}

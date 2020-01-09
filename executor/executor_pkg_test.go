// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package executor

import (
	. "github.com/pingcap/check"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/util/mock"
	"github.com/pingcap/tidb/util/ranger"
)

var _ = Suite(&testExecSuite{})

type testExecSuite struct {
}

func (s *testExecSuite) TestBuildKvRangesForIndexJoinWithoutCwc(c *C) {
	indexRanges := make([]*ranger.Range, 0, 6)
	indexRanges = append(indexRanges, generateIndexRange(1, 1, 1, 1, 1))
	indexRanges = append(indexRanges, generateIndexRange(1, 1, 2, 1, 1))
	indexRanges = append(indexRanges, generateIndexRange(1, 1, 2, 1, 2))
	indexRanges = append(indexRanges, generateIndexRange(1, 1, 3, 1, 1))
	indexRanges = append(indexRanges, generateIndexRange(2, 1, 1, 1, 1))
	indexRanges = append(indexRanges, generateIndexRange(2, 1, 2, 1, 1))

	joinKeyRows := make([]*indexJoinLookUpContent, 0, 5)
	joinKeyRows = append(joinKeyRows, &indexJoinLookUpContent{keys: generateDatumSlice(1, 1)})
	joinKeyRows = append(joinKeyRows, &indexJoinLookUpContent{keys: generateDatumSlice(1, 2)})
	joinKeyRows = append(joinKeyRows, &indexJoinLookUpContent{keys: generateDatumSlice(2, 1)})
	joinKeyRows = append(joinKeyRows, &indexJoinLookUpContent{keys: generateDatumSlice(2, 2)})
	joinKeyRows = append(joinKeyRows, &indexJoinLookUpContent{keys: generateDatumSlice(2, 3)})

	keyOff2IdxOff := []int{1, 3}
	ctx := mock.NewContext()
	kvRanges, err := buildKvRangesForIndexJoin(ctx, 0, 0, joinKeyRows, indexRanges, keyOff2IdxOff, nil)
	c.Assert(err, IsNil)
	// Check the kvRanges is in order.
	for i, kvRange := range kvRanges {
		c.Assert(kvRange.StartKey.Cmp(kvRange.EndKey) < 0, IsTrue)
		if i > 0 {
			c.Assert(kvRange.StartKey.Cmp(kvRanges[i-1].EndKey) >= 0, IsTrue)
		}
	}
}

func generateIndexRange(vals ...int64) *ranger.Range {
	lowDatums := generateDatumSlice(vals...)
	highDatums := make([]types.Datum, len(vals))
	copy(highDatums, lowDatums)
	return &ranger.Range{LowVal: lowDatums, HighVal: highDatums}
}

func generateDatumSlice(vals ...int64) []types.Datum {
	datums := make([]types.Datum, len(vals))
	for i, val := range vals {
		datums[i].SetInt64(val)
	}
	return datums
}

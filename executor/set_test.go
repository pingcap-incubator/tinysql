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

package executor_test

import (
	. "github.com/pingcap/check"
	"github.com/pingcap/parser/terror"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/util/testkit"
)

func (s *testSuite5) TestSetCharset(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec(`SET NAMES latin1`)

	ctx := tk.Se.(sessionctx.Context)
	sessionVars := ctx.GetSessionVars()
	for _, v := range variable.SetNamesVariables {
		sVar, err := variable.GetSessionSystemVar(sessionVars, v)
		c.Assert(err, IsNil)
		c.Assert(sVar != "utf8", IsTrue)
	}
	tk.MustExec(`SET NAMES utf8`)
	for _, v := range variable.SetNamesVariables {
		sVar, err := variable.GetSessionSystemVar(sessionVars, v)
		c.Assert(err, IsNil)
		c.Assert(sVar, Equals, "utf8")
	}
	sVar, err := variable.GetSessionSystemVar(sessionVars, variable.CollationConnection)
	c.Assert(err, IsNil)
	c.Assert(sVar, Equals, "utf8_bin")

	// Issue 1523
	tk.MustExec(`SET NAMES binary`)
}

func (s *testSuite5) TestSelectGlobalVar(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	tk.MustQuery("select @@global.max_connections;").Check(testkit.Rows("151"))
	tk.MustQuery("select @@max_connections;").Check(testkit.Rows("151"))

	tk.MustExec("set @@global.max_connections=100;")

	tk.MustQuery("select @@global.max_connections;").Check(testkit.Rows("100"))
	tk.MustQuery("select @@max_connections;").Check(testkit.Rows("100"))

	tk.MustExec("set @@global.max_connections=151;")

	// test for unknown variable.
	err := tk.ExecToErr("select @@invalid")
	c.Assert(terror.ErrorEqual(err, variable.ErrUnknownSystemVar), IsTrue, Commentf("err %v", err))
	err = tk.ExecToErr("select @@global.invalid")
	c.Assert(terror.ErrorEqual(err, variable.ErrUnknownSystemVar), IsTrue, Commentf("err %v", err))
}

func (s *testSuite5) TestEnableNoopFunctionsVar(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	// test for tidb_enable_noop_functions
	tk.MustQuery(`select @@global.tidb_enable_noop_functions;`).Check(testkit.Rows("0"))
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("0"))

	_, err := tk.Exec(`select get_lock('lock1', 2);`)
	c.Assert(terror.ErrorEqual(err, expression.ErrFunctionsNoopImpl), IsTrue, Commentf("err %v", err))
	_, err = tk.Exec(`select release_lock('lock1');`)
	c.Assert(terror.ErrorEqual(err, expression.ErrFunctionsNoopImpl), IsTrue, Commentf("err %v", err))

	// change session var to 1
	tk.MustExec(`set tidb_enable_noop_functions=1;`)
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("1"))
	tk.MustQuery(`select @@global.tidb_enable_noop_functions;`).Check(testkit.Rows("0"))
	tk.MustQuery(`select get_lock("lock", 10)`).Check(testkit.Rows("1"))
	tk.MustQuery(`select release_lock("lock")`).Check(testkit.Rows("1"))

	// restore to 0
	tk.MustExec(`set tidb_enable_noop_functions=0;`)
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("0"))
	tk.MustQuery(`select @@global.tidb_enable_noop_functions;`).Check(testkit.Rows("0"))

	_, err = tk.Exec(`select get_lock('lock2', 10);`)
	c.Assert(terror.ErrorEqual(err, expression.ErrFunctionsNoopImpl), IsTrue, Commentf("err %v", err))
	_, err = tk.Exec(`select release_lock('lock2');`)
	c.Assert(terror.ErrorEqual(err, expression.ErrFunctionsNoopImpl), IsTrue, Commentf("err %v", err))

	// set test
	_, err = tk.Exec(`set tidb_enable_noop_functions='abc'`)
	c.Assert(err, NotNil)
	_, err = tk.Exec(`set tidb_enable_noop_functions=11`)
	c.Assert(err, NotNil)
	tk.MustExec(`set tidb_enable_noop_functions="off";`)
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("0"))
	tk.MustExec(`set tidb_enable_noop_functions="on";`)
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("1"))
	tk.MustExec(`set tidb_enable_noop_functions=0;`)
	tk.MustQuery(`select @@tidb_enable_noop_functions;`).Check(testkit.Rows("0"))
}

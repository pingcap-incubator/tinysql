// Copyright 2015 PingCAP, Inc.
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
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/protobuf/proto"
	. "github.com/pingcap/check"
	"github.com/pingcap/errors"
	"github.com/pingcap/failpoint"
	pb "github.com/pingcap/kvproto/pkg/kvrpcpb"
	"github.com/pingcap/parser"
	"github.com/pingcap/parser/model"
	"github.com/pingcap/parser/mysql"
	"github.com/pingcap/parser/terror"
	"github.com/pingcap/tidb/config"
	"github.com/pingcap/tidb/ddl"
	"github.com/pingcap/tidb/domain"
	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/kv"
	"github.com/pingcap/tidb/meta"
	"github.com/pingcap/tidb/meta/autoid"
	plannercore "github.com/pingcap/tidb/planner/core"
	"github.com/pingcap/tidb/session"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/store/mockstore"
	"github.com/pingcap/tidb/store/mockstore/mocktikv"
	"github.com/pingcap/tidb/store/tikv"
	"github.com/pingcap/tidb/store/tikv/oracle"
	"github.com/pingcap/tidb/store/tikv/tikvrpc"
	"github.com/pingcap/tidb/table"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/util/logutil"
	"github.com/pingcap/tidb/util/mock"
	"github.com/pingcap/tidb/util/testkit"
	"github.com/pingcap/tidb/util/testleak"
	"github.com/pingcap/tidb/util/timeutil"
	"github.com/pingcap/tipb/go-tipb"
)

func TestT(t *testing.T) {
	CustomVerboseFlag = true
	*CustomParallelSuiteFlag = true
	logLevel := os.Getenv("log_level")
	logutil.InitLogger(logutil.NewLogConfig(logLevel, logutil.DefaultLogFormat, logutil.EmptyFileLogConfig, false))
	autoid.SetStep(5000)

	old := config.GetGlobalConfig()
	new := *old
	config.StoreGlobalConfig(&new)

	testleak.BeforeTest()
	TestingT(t)
	testleak.AfterTestT(t)()
}

var _ = Suite(&testSuite{&baseTestSuite{}})
var _ = Suite(&testSuiteP1{&baseTestSuite{}})
var _ = Suite(&testSuiteP2{&baseTestSuite{}})
var _ = Suite(&testSuite1{})
var _ = Suite(&testSuite2{&baseTestSuite{}})
var _ = Suite(&testSuite3{&baseTestSuite{}})
var _ = Suite(&testSuite4{&baseTestSuite{}})
var _ = Suite(&testSuite5{&baseTestSuite{}})
var _ = Suite(&testSuiteJoin1{&baseTestSuite{}})
var _ = Suite(&testSuiteJoin2{&baseTestSuite{}})
var _ = Suite(&testSuiteJoin3{&baseTestSuite{}})
var _ = Suite(&testSuiteAgg{baseTestSuite: &baseTestSuite{}})
var _ = Suite(&testSuite6{&baseTestSuite{}})
var _ = Suite(&testSuite7{&baseTestSuite{}})
var _ = Suite(&testSuite8{&baseTestSuite{}})
var _ = Suite(&testBypassSuite{})
var _ = Suite(&testUpdateSuite{})

type testSuite struct{ *baseTestSuite }
type testSuiteP1 struct{ *baseTestSuite }
type testSuiteP2 struct{ *baseTestSuite }

type baseTestSuite struct {
	cluster   *mocktikv.Cluster
	mvccStore mocktikv.MVCCStore
	store     kv.Storage
	domain    *domain.Domain
	*parser.Parser
	ctx *mock.Context
}

var mockTikv = flag.Bool("mockTikv", true, "use mock tikv store in executor test")

func (s *baseTestSuite) SetUpSuite(c *C) {
	s.Parser = parser.New()
	flag.Lookup("mockTikv")
	useMockTikv := *mockTikv
	if useMockTikv {
		s.cluster = mocktikv.NewCluster()
		mocktikv.BootstrapWithSingleStore(s.cluster)
		s.mvccStore = mocktikv.MustNewMVCCStore()
		store, err := mockstore.NewMockTikvStore(
			mockstore.WithCluster(s.cluster),
			mockstore.WithMVCCStore(s.mvccStore),
		)
		c.Assert(err, IsNil)
		s.store = store
		session.SetSchemaLease(0)
		session.DisableStats4Test()
	}
	d, err := session.BootstrapSession(s.store)
	c.Assert(err, IsNil)
	s.domain = d
}

func (s *baseTestSuite) TearDownSuite(c *C) {
	s.domain.Close()
	s.store.Close()
}

func (s *testSuite) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

func (s *testSuiteP1) TestChange(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int)")
	tk.MustExec("alter table t change a b int")
	tk.MustExec("alter table t change b c bigint")
	c.Assert(tk.ExecToErr("alter table t change c d varchar(100)"), NotNil)
}

func (s *testSuiteP2) TestAdminShowDDLJobs(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("create database if not exists test_admin_show_ddl_jobs")
	tk.MustExec("use test_admin_show_ddl_jobs")
	tk.MustExec("create table t (a int);")

	re := tk.MustQuery("admin show ddl jobs 1")
	row := re.Rows()[0]
	c.Assert(row[1], Equals, "test_admin_show_ddl_jobs")
	jobID, err := strconv.Atoi(row[0].(string))
	c.Assert(err, IsNil)

	c.Assert(tk.Se.NewTxn(context.Background()), IsNil)
	txn, err := tk.Se.Txn(true)
	c.Assert(err, IsNil)
	t := meta.NewMeta(txn)
	job, err := t.GetHistoryDDLJob(int64(jobID))
	c.Assert(err, IsNil)
	c.Assert(job, NotNil)
	job.SchemaName = ""
	err = t.AddHistoryDDLJob(job, true)
	c.Assert(err, IsNil)
	err = tk.Se.CommitTxn(context.Background())
	c.Assert(err, IsNil)

	re = tk.MustQuery("admin show ddl jobs 1")
	row = re.Rows()[0]
	c.Assert(row[1], Equals, "test_admin_show_ddl_jobs")

	re = tk.MustQuery("admin show ddl jobs 1 where job_type='create table'")
	row = re.Rows()[0]
	c.Assert(row[1], Equals, "test_admin_show_ddl_jobs")
}

func (s *baseTestSuite) fillData(tk *testkit.TestKit, table string) {
	tk.MustExec("use test")
	tk.MustExec(fmt.Sprintf("create table %s(id int not null default 1, name varchar(255), PRIMARY KEY(id));", table))

	// insert data
	tk.MustExec(fmt.Sprintf("insert INTO %s VALUES (1, \"hello\");", table))
	tk.CheckExecResult(1, 0)
	tk.MustExec(fmt.Sprintf("insert into %s values (2, \"hello\");", table))
	tk.CheckExecResult(1, 0)
}

func (s *testSuiteP1) TestSelectWithoutFrom(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	r := tk.MustQuery("select 1 + 2*3;")
	r.Check(testkit.Rows("7"))

	r = tk.MustQuery(`select _utf8"string";`)
	r.Check(testkit.Rows("string"))

	r = tk.MustQuery("select 1 order by 1;")
	r.Check(testkit.Rows("1"))
}

// TestSelectBackslashN Issue 3685.
func (s *testSuiteP1) TestSelectBackslashN(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	sql := `select \N;`
	r := tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err := tk.Exec(sql)
	c.Check(err, IsNil)
	fields := rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "NULL")

	sql = `select "\N";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("N"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `N`)

	tk.MustExec("use test;")
	tk.MustExec("create table test (`\\N` int);")
	tk.MustExec("insert into test values (1);")
	tk.CheckExecResult(1, 0)
	sql = "select * from test;"
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("1"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `\N`)

	sql = `select \N from test;`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(err, IsNil)
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `NULL`)

	sql = `select (\N) from test;`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `NULL`)

	sql = "select `\\N` from test;"
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("1"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `\N`)

	sql = "select (`\\N`) from test;"
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("1"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `\N`)

	sql = `select '\N' from test;`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("N"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `N`)

	sql = `select ('\N') from test;`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("N"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `N`)
}

// TestSelectNull Issue #4053.
func (s *testSuiteP1) TestSelectNull(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	sql := `select nUll;`
	r := tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err := tk.Exec(sql)
	c.Check(err, IsNil)
	fields := rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `NULL`)

	sql = `select (null);`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `NULL`)

	sql = `select null+NULL;`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("<nil>"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(err, IsNil)
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `null+NULL`)
}

// TestSelectStringLiteral Issue #3686.
func (s *testSuiteP1) TestSelectStringLiteral(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	sql := `select 'abc';`
	r := tk.MustQuery(sql)
	r.Check(testkit.Rows("abc"))
	rs, err := tk.Exec(sql)
	c.Check(err, IsNil)
	fields := rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `abc`)

	sql = `select (('abc'));`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("abc"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `abc`)

	sql = `select 'abc'+'def';`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("0"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, `'abc'+'def'`)

	// Below checks whether leading invalid chars are trimmed.
	sql = "select '\n';"
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("\n"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "")

	sql = "select '\t   col';" // Lowercased letter is a valid char.
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "col")

	sql = "select '\t   Col';" // Uppercased letter is a valid char.
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "Col")

	sql = "select '\n\t   中文 col';" // Chinese char is a valid char.
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "中文 col")

	sql = "select ' \r\n  .col';" // Punctuation is a valid char.
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, ".col")

	sql = "select '   😆col';" // Emoji is a valid char.
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "😆col")

	// Below checks whether trailing invalid chars are preserved.
	sql = `select 'abc   ';`
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "abc   ")

	sql = `select '  abc   123   ';`
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "abc   123   ")

	// Issue #4239.
	sql = `select 'a' ' ' 'string';`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("a string"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "a")

	sql = `select 'a' " " "string";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("a string"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "a")

	sql = `select 'string' 'string';`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("stringstring"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "string")

	sql = `select "ss" "a";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("ssa"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "ss")

	sql = `select "ss" "a" "b";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("ssab"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "ss")

	sql = `select "ss" "a" ' ' "b";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("ssa b"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "ss")

	sql = `select "ss" "a" ' ' "b" ' ' "d";`
	r = tk.MustQuery(sql)
	r.Check(testkit.Rows("ssa b d"))
	rs, err = tk.Exec(sql)
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.O, Equals, "ss")
}

func (s *testSuiteP1) TestSelectLimit(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	s.fillData(tk, "select_limit")

	tk.MustExec("insert INTO select_limit VALUES (3, \"hello\");")
	tk.CheckExecResult(1, 0)
	tk.MustExec("insert INTO select_limit VALUES (4, \"hello\");")
	tk.CheckExecResult(1, 0)

	r := tk.MustQuery("select * from select_limit limit 1;")
	r.Check(testkit.Rows("1 hello"))

	r = tk.MustQuery("select id from (select * from select_limit limit 1) k where id != 1;")
	r.Check(testkit.Rows())

	r = tk.MustQuery("select * from select_limit limit 18446744073709551615 offset 0;")
	r.Check(testkit.Rows("1 hello", "2 hello", "3 hello", "4 hello"))

	r = tk.MustQuery("select * from select_limit limit 18446744073709551615 offset 1;")
	r.Check(testkit.Rows("2 hello", "3 hello", "4 hello"))

	r = tk.MustQuery("select * from select_limit limit 18446744073709551615 offset 3;")
	r.Check(testkit.Rows("4 hello"))

	err := tk.ExecToErr("select * from select_limit limit 18446744073709551616 offset 3;")
	c.Assert(err, NotNil)
}

func (s *testSuiteP1) TestSelectOrderBy(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	s.fillData(tk, "select_order_test")

	// Test star field
	r := tk.MustQuery("select * from select_order_test where id = 1 order by id limit 1 offset 0;")
	r.Check(testkit.Rows("1 hello"))

	r = tk.MustQuery("select id from select_order_test order by id desc limit 1 ")
	r.Check(testkit.Rows("2"))

	r = tk.MustQuery("select id from select_order_test order by id + 1 desc limit 1 ")
	r.Check(testkit.Rows("2"))

	// Test limit
	r = tk.MustQuery("select * from select_order_test order by name, id limit 1 offset 0;")
	r.Check(testkit.Rows("1 hello"))

	// Test limit
	r = tk.MustQuery("select id as c1, name from select_order_test order by 2, id limit 1 offset 0;")
	r.Check(testkit.Rows("1 hello"))

	// Test limit overflow
	r = tk.MustQuery("select * from select_order_test order by name, id limit 100 offset 0;")
	r.Check(testkit.Rows("1 hello", "2 hello"))

	// Test offset overflow
	r = tk.MustQuery("select * from select_order_test order by name, id limit 1 offset 100;")
	r.Check(testkit.Rows())

	// Test limit exceeds int range.
	r = tk.MustQuery("select id from select_order_test order by name, id limit 18446744073709551615;")
	r.Check(testkit.Rows("1", "2"))

	// Test multiple field
	r = tk.MustQuery("select id, name from select_order_test where id = 1 group by id, name limit 1 offset 0;")
	r.Check(testkit.Rows("1 hello"))

	// Test limit + order by
	for i := 3; i <= 10; i += 1 {
		tk.MustExec(fmt.Sprintf("insert INTO select_order_test VALUES (%d, \"zz\");", i))
	}
	tk.MustExec("insert INTO select_order_test VALUES (10086, \"hi\");")
	for i := 11; i <= 20; i += 1 {
		tk.MustExec(fmt.Sprintf("insert INTO select_order_test VALUES (%d, \"hh\");", i))
	}
	for i := 21; i <= 30; i += 1 {
		tk.MustExec(fmt.Sprintf("insert INTO select_order_test VALUES (%d, \"zz\");", i))
	}
	tk.MustExec("insert INTO select_order_test VALUES (1501, \"aa\");")
	r = tk.MustQuery("select * from select_order_test order by name, id limit 1 offset 3;")
	r.Check(testkit.Rows("11 hh"))
	tk.MustExec("drop table select_order_test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c int, d int)")
	tk.MustExec("insert t values (1, 1)")
	tk.MustExec("insert t values (1, 2)")
	tk.MustExec("insert t values (1, 3)")
	r = tk.MustQuery("select 1-d as d from t order by d;")
	r.Check(testkit.Rows("-2", "-1", "0"))
	r = tk.MustQuery("select 1-d as d from t order by d + 1;")
	r.Check(testkit.Rows("0", "-1", "-2"))
	r = tk.MustQuery("select t.d from t order by d;")
	r.Check(testkit.Rows("1", "2", "3"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int, b int, c int)")
	tk.MustExec("insert t values (1, 2, 3)")
	r = tk.MustQuery("select b from (select a,b from t order by a,c) t")
	r.Check(testkit.Rows("2"))
	r = tk.MustQuery("select b from (select a,b from t order by a,c limit 1) t")
	r.Check(testkit.Rows("2"))
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b int, index idx(a))")
	tk.MustExec("insert into t values(1, 1), (2, 2)")
	tk.MustQuery("select * from t where 1 order by b").Check(testkit.Rows("1 1", "2 2"))
	tk.MustQuery("select * from t where a between 1 and 2 order by a desc").Check(testkit.Rows("2 2", "1 1"))

	// Test double read and topN is pushed down to first read plannercore.
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int primary key, b int, c int, index idx(b))")
	tk.MustExec("insert into t values(1, 3, 1)")
	tk.MustExec("insert into t values(2, 2, 2)")
	tk.MustExec("insert into t values(3, 1, 3)")
	tk.MustQuery("select * from t use index(idx) order by a desc limit 1").Check(testkit.Rows("3 1 3"))

	// Test double read which needs to keep order.
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b int, key b (b))")
	tk.Se.GetSessionVars().IndexLookupSize = 3
	for i := 0; i < 10; i++ {
		tk.MustExec(fmt.Sprintf("insert into t values(%d, %d)", i, 10-i))
	}
	tk.MustQuery("select a from t use index(b) order by b").Check(testkit.Rows("9", "8", "7", "6", "5", "4", "3", "2", "1", "0"))
}

func (s *testSuiteP1) TestOrderBy(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 int, c2 int, c3 varchar(20))")
	tk.MustExec("insert into t values (1, 2, 'abc'), (2, 1, 'bcd')")

	// Fix issue https://github.com/pingcap/tidb/issues/337
	tk.MustQuery("select c1 as a, c1 as b from t order by c1").Check(testkit.Rows("1 1", "2 2"))

	tk.MustQuery("select c1 as a, t.c1 as a from t order by a desc").Check(testkit.Rows("2 2", "1 1"))
	tk.MustQuery("select c1 as c2 from t order by c2").Check(testkit.Rows("1", "2"))
	tk.MustQuery("select sum(c1) from t order by sum(c1)").Check(testkit.Rows("3"))
	tk.MustQuery("select c1 as c2 from t order by c2 + 1").Check(testkit.Rows("2", "1"))

	// Order by position.
	tk.MustQuery("select * from t order by 1").Check(testkit.Rows("1 2 abc", "2 1 bcd"))
	tk.MustQuery("select * from t order by 2").Check(testkit.Rows("2 1 bcd", "1 2 abc"))

	// Order by binary.
	tk.MustQuery("select c1, c3 from t order by binary c1 desc").Check(testkit.Rows("2 bcd", "1 abc"))
	tk.MustQuery("select c1, c2 from t order by binary c3").Check(testkit.Rows("1 2", "2 1"))
}

func (s *testSuiteP1) TestSelectErrorRow(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	err := tk.ExecToErr("select row(1, 1) from test")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test group by row(1, 1);")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test order by row(1, 1);")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test having row(1, 1);")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select (select 1, 1) from test;")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test group by (select 1, 1);")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test order by (select 1, 1);")
	c.Assert(err, NotNil)

	err = tk.ExecToErr("select * from test having (select 1, 1);")
	c.Assert(err, NotNil)
}

// TestIssue345 is related with https://github.com/pingcap/tidb/issues/345
func (s *testSuiteP1) TestIssue345(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t1, t2`)
	tk.MustExec(`create table t1 (c1 int);`)
	tk.MustExec(`create table t2 (c2 int);`)
	tk.MustExec(`insert into t1 values (1);`)
	tk.MustExec(`insert into t2 values (2);`)
	tk.MustExec(`update t1, t2 set t1.c1 = 2, t2.c2 = 1;`)
	tk.MustExec(`update t1, t2 set c1 = 2, c2 = 1;`)
	tk.MustExec(`update t1 as a, t2 as b set a.c1 = 2, b.c2 = 1;`)

	// Check t1 content
	r := tk.MustQuery("SELECT * FROM t1;")
	r.Check(testkit.Rows("2"))
	// Check t2 content
	r = tk.MustQuery("SELECT * FROM t2;")
	r.Check(testkit.Rows("1"))

	tk.MustExec(`update t1 as a, t2 as t1 set a.c1 = 1, t1.c2 = 2;`)
	// Check t1 content
	r = tk.MustQuery("SELECT * FROM t1;")
	r.Check(testkit.Rows("1"))
	// Check t2 content
	r = tk.MustQuery("SELECT * FROM t2;")
	r.Check(testkit.Rows("2"))

	_, err := tk.Exec(`update t1 as a, t2 set t1.c1 = 10;`)
	c.Assert(err, NotNil)
}

func (s *testSuiteP1) TestIssue5055(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t1, t2`)
	tk.MustExec(`create table t1 (a int);`)
	tk.MustExec(`create table t2 (a int);`)
	tk.MustExec(`insert into t1 values(1);`)
	tk.MustExec(`insert into t2 values(1);`)
	result := tk.MustQuery("select tbl1.* from (select t1.a, 1 from t1) tbl1 left join t2 tbl2 on tbl1.a = tbl2.a order by tbl1.a desc limit 1;")
	result.Check(testkit.Rows("1 1"))
}

func (s *testSuiteP2) TestUnion(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	testSQL := `drop table if exists union_test; create table union_test(id int);`
	tk.MustExec(testSQL)

	testSQL = `drop table if exists union_test;`
	tk.MustExec(testSQL)
	testSQL = `create table union_test(id int);`
	tk.MustExec(testSQL)
	testSQL = `insert union_test values (1),(2)`
	tk.MustExec(testSQL)

	testSQL = `select * from (select id from union_test union select id from union_test) t order by id;`
	r := tk.MustQuery(testSQL)
	r.Check(testkit.Rows("1", "2"))

	r = tk.MustQuery("select 1 union all select 1")
	r.Check(testkit.Rows("1", "1"))

	r = tk.MustQuery("select 1 union all select 1 union select 1")
	r.Check(testkit.Rows("1"))

	r = tk.MustQuery("select 1 as a union (select 2) order by a limit 1")
	r.Check(testkit.Rows("1"))

	r = tk.MustQuery("select 1 as a union (select 2) order by a limit 1, 1")
	r.Check(testkit.Rows("2"))

	r = tk.MustQuery("select id from union_test union all (select 1) order by id desc")
	r.Check(testkit.Rows("2", "1", "1"))

	r = tk.MustQuery("select id as a from union_test union (select 1) order by a desc")
	r.Check(testkit.Rows("2", "1"))

	r = tk.MustQuery(`select null as a union (select "abc") order by a`)
	r.Check(testkit.Rows("<nil>", "abc"))

	r = tk.MustQuery(`select "abc" as a union (select 1) order by a`)
	r.Check(testkit.Rows("1", "abc"))

	tk.MustExec("drop table if exists t1")
	tk.MustExec("create table t1 (c int, d int)")
	tk.MustExec("insert t1 values (NULL, 1)")
	tk.MustExec("insert t1 values (1, 1)")
	tk.MustExec("insert t1 values (1, 2)")
	tk.MustExec("drop table if exists t2")
	tk.MustExec("create table t2 (c int, d int)")
	tk.MustExec("insert t2 values (1, 3)")
	tk.MustExec("insert t2 values (1, 1)")
	tk.MustExec("drop table if exists t3")
	tk.MustExec("create table t3 (c int, d int)")
	tk.MustExec("insert t3 values (3, 2)")
	tk.MustExec("insert t3 values (4, 3)")
	r = tk.MustQuery(`select sum(c1), c2 from (select c c1, d c2 from t1 union all select d c1, c c2 from t2 union all select c c1, d c2 from t3) x group by c2 order by c2`)
	r.Check(testkit.Rows("5 1", "4 2", "4 3"))

	tk.MustExec("drop table if exists t1, t2, t3")
	tk.MustExec("create table t1 (a int primary key)")
	tk.MustExec("create table t2 (a int primary key)")
	tk.MustExec("create table t3 (a int primary key)")
	tk.MustExec("insert t1 values (7), (8)")
	tk.MustExec("insert t2 values (1), (9)")
	tk.MustExec("insert t3 values (2), (3)")
	r = tk.MustQuery("select * from t1 union all select * from t2 union all (select * from t3) order by a limit 2")
	r.Check(testkit.Rows("1", "2"))

	tk.MustExec("drop table if exists t1, t2")
	tk.MustExec("create table t1 (a int)")
	tk.MustExec("create table t2 (a int)")
	tk.MustExec("insert t1 values (2), (1)")
	tk.MustExec("insert t2 values (3), (4)")
	r = tk.MustQuery("select * from t1 union all (select * from t2) order by a limit 1")
	r.Check(testkit.Rows("1"))
	r = tk.MustQuery("select (select * from t1 where a != t.a union all (select * from t2 where a != t.a) order by a limit 1) from t1 t")
	r.Check(testkit.Rows("1", "2"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (id int unsigned primary key auto_increment, c1 int, c2 int, index c1_c2 (c1, c2))")
	tk.MustExec("insert into t (c1, c2) values (1, 1)")
	tk.MustExec("insert into t (c1, c2) values (1, 2)")
	tk.MustExec("insert into t (c1, c2) values (2, 3)")
	r = tk.MustQuery("select * from (select * from t where t.c1 = 1 union select * from t where t.id = 1) s order by s.id")
	r.Check(testkit.Rows("1 1 1", "2 1 2"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (f1 DATE)")
	tk.MustExec("INSERT INTO t VALUES ('1978-11-26')")
	r = tk.MustQuery("SELECT f1+0 FROM t UNION SELECT f1+0 FROM t")
	r.Check(testkit.Rows("19781126"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (a int, b int)")
	tk.MustExec("INSERT INTO t VALUES ('1', '1')")
	r = tk.MustQuery("select b from (SELECT * FROM t UNION ALL SELECT a, b FROM t order by a) t")
	r.Check(testkit.Rows("1", "1"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (a DECIMAL(4,2))")
	tk.MustExec("INSERT INTO t VALUE(12.34)")
	r = tk.MustQuery("SELECT 1 AS c UNION select a FROM t")
	r.Sort().Check(testkit.Rows("1.00", "12.34"))

	// #issue3771
	r = tk.MustQuery("SELECT 'a' UNION SELECT CONCAT('a', -4)")
	r.Sort().Check(testkit.Rows("a", "a-4"))

	// test race
	tk.MustQuery("SELECT @x:=0 UNION ALL SELECT @x:=0 UNION ALL SELECT @x")

	// test field tp
	tk.MustExec("drop table if exists t1, t2")
	tk.MustExec("CREATE TABLE t1 (a date)")
	tk.MustExec("CREATE TABLE t2 (a date)")
	tk.MustExec("SELECT a from t1 UNION select a FROM t2")
	tk.MustQuery("show create table t1").Check(testkit.Rows("t1 CREATE TABLE `t1` (\n" + "  `a` date DEFAULT NULL\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))

	// Move from session test.
	tk.MustExec("drop table if exists t1, t2")
	tk.MustExec("create table t1 (c double);")
	tk.MustExec("create table t2 (c double);")
	tk.MustExec("insert into t1 value (73);")
	tk.MustExec("insert into t2 value (930);")
	// If set unspecified column flen to 0, it will cause bug in union.
	// This test is used to prevent the bug reappear.
	tk.MustQuery("select c from t1 union (select c from t2) order by c").Check(testkit.Rows("73", "930"))

	// issue 5703
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a date)")
	tk.MustExec("insert into t value ('2017-01-01'), ('2017-01-02')")
	r = tk.MustQuery("(select a from t where a < 0) union (select a from t where a > 0) order by a")
	r.Check(testkit.Rows("2017-01-01", "2017-01-02"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int)")
	tk.MustExec("insert into t value(0),(0)")
	tk.MustQuery("select 1 from (select a from t union all select a from t) tmp").Check(testkit.Rows("1", "1", "1", "1"))
	tk.MustQuery("select 10 as a from dual union select a from t order by a desc limit 1 ").Check(testkit.Rows("10"))
	tk.MustQuery("select -10 as a from dual union select a from t order by a limit 1 ").Check(testkit.Rows("-10"))
	tk.MustQuery("select count(1) from (select a from t union all select a from t) tmp").Check(testkit.Rows("4"))

	err := tk.ExecToErr("select 1 from (select a from t limit 1 union all select a from t limit 1) tmp")
	c.Assert(err, NotNil)
	terr := errors.Cause(err).(*terror.Error)
	c.Assert(terr.Code(), Equals, terror.ErrCode(mysql.ErrWrongUsage))

	err = tk.ExecToErr("select 1 from (select a from t order by a union all select a from t limit 1) tmp")
	c.Assert(err, NotNil)
	terr = errors.Cause(err).(*terror.Error)
	c.Assert(terr.Code(), Equals, terror.ErrCode(mysql.ErrWrongUsage))

	_, err = tk.Exec("(select a from t order by a) union all select a from t limit 1 union all select a from t limit 1")
	c.Assert(terror.ErrorEqual(err, plannercore.ErrWrongUsage), IsTrue, Commentf("err %v", err))

	_, err = tk.Exec("(select a from t limit 1) union all select a from t limit 1")
	c.Assert(err, IsNil)
	_, err = tk.Exec("(select a from t order by a) union all select a from t order by a")
	c.Assert(err, IsNil)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int)")
	tk.MustExec("insert into t value(1),(2),(3)")

	tk.MustQuery("(select a from t order by a limit 2) union all (select a from t order by a desc limit 2) order by a desc limit 1,2").Check(testkit.Rows("2", "2"))
	tk.MustQuery("select a from t union all select a from t order by a desc limit 5").Check(testkit.Rows("3", "3", "2", "2", "1"))
	tk.MustQuery("(select a from t order by a desc limit 2) union all select a from t group by a order by a").Check(testkit.Rows("1", "2", "2", "3", "3"))
	tk.MustQuery("(select a from t order by a desc limit 2) union all select 33 as a order by a desc limit 2").Check(testkit.Rows("33", "3"))

	tk.MustQuery("select 1 union select 1 union all select 1").Check(testkit.Rows("1", "1"))
	tk.MustQuery("select 1 union all select 1 union select 1").Check(testkit.Rows("1"))

	tk.MustExec("drop table if exists t1, t2")
	tk.MustExec(`create table t1(a bigint, b bigint);`)
	tk.MustExec(`create table t2(a bigint, b bigint);`)
	tk.MustExec(`insert into t1 values(1, 1);`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t1 select * from t1;`)
	tk.MustExec(`insert into t2 values(1, 1);`)
	tk.MustExec(`set @@tidb_init_chunk_size=2;`)
	tk.MustExec(`set @@sql_mode="";`)
	tk.MustQuery(`select count(*) from (select t1.a, t1.b from t1 left join t2 on t1.a=t2.a union all select t1.a, t1.a from t1 left join t2 on t1.a=t2.a) tmp;`).Check(testkit.Rows("128"))
	tk.MustQuery(`select tmp.a, count(*) from (select t1.a, t1.b from t1 left join t2 on t1.a=t2.a union all select t1.a, t1.a from t1 left join t2 on t1.a=t2.a) tmp;`).Check(testkit.Rows("1 128"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b int)")
	tk.MustExec("insert into t value(1 ,2)")
	tk.MustQuery("select a, b from (select a, 0 as d, b from t union all select a, 0 as d, b from t) test;").Check(testkit.Rows("1 2", "1 2"))

	// #issue 8141
	tk.MustExec("drop table if exists t1")
	tk.MustExec("create table t1(a int, b int)")
	tk.MustExec("insert into t1 value(1,2),(1,1),(2,2),(2,2),(3,2),(3,2)")
	tk.MustExec("set @@tidb_init_chunk_size=2;")
	tk.MustQuery("select count(*) from (select a as c, a as d from t1 union all select a, b from t1) t;").Check(testkit.Rows("12"))

	// #issue 8189 and #issue 8199
	tk.MustExec("drop table if exists t1")
	tk.MustExec("drop table if exists t2")
	tk.MustExec("CREATE TABLE t1 (a int not null, b char (10) not null)")
	tk.MustExec("insert into t1 values(1,'a'),(2,'b'),(3,'c'),(3,'c')")
	tk.MustExec("CREATE TABLE t2 (a int not null, b char (10) not null)")
	tk.MustExec("insert into t2 values(1,'a'),(2,'b'),(3,'c'),(3,'c')")
	tk.MustQuery("select a from t1 union select a from t1 order by (select a+1);").Check(testkit.Rows("1", "2", "3"))

	// #issue 8201
	for i := 0; i < 4; i++ {
		tk.MustQuery("SELECT(SELECT 0 AS a FROM dual UNION SELECT 1 AS a FROM dual ORDER BY a ASC  LIMIT 1) AS dev").Check(testkit.Rows("0"))
	}

	// #issue 8231
	tk.MustExec("drop table if exists t1")
	tk.MustExec("CREATE TABLE t1 (uid int(1))")
	tk.MustExec("INSERT INTO t1 SELECT 150")
	tk.MustQuery("SELECT 'a' UNION SELECT uid FROM t1 order by 1 desc;").Check(testkit.Rows("a", "150"))

	// #issue 8196
	tk.MustExec("drop table if exists t1")
	tk.MustExec("drop table if exists t2")
	tk.MustExec("CREATE TABLE t1 (a int not null, b char (10) not null)")
	tk.MustExec("insert into t1 values(1,'a'),(2,'b'),(3,'c'),(3,'c')")
	tk.MustExec("CREATE TABLE t2 (a int not null, b char (10) not null)")
	tk.MustExec("insert into t2 values(3,'c'),(4,'d'),(5,'f'),(6,'e')")
	tk.MustExec("analyze table t1")
	tk.MustExec("analyze table t2")
	_, err = tk.Exec("(select a,b from t1 limit 2) union all (select a,b from t2 order by a limit 1) order by t1.b")
	c.Assert(err.Error(), Equals, "[planner:1250]Table 't1' from one of the SELECTs cannot be used in global ORDER clause")

	// #issue 9900
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b decimal(6, 3))")
	tk.MustExec("insert into t values(1, 1.000)")
	tk.MustQuery("select count(distinct a), sum(distinct a), avg(distinct a) from (select a from t union all select b from t) tmp;").Check(testkit.Rows("1 1.000 1.0000000"))
}

func (s *testSuiteP1) TestNeighbouringProj(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	tk.MustExec("drop table if exists t1, t2")
	tk.MustExec("create table t1(a int, b int)")
	tk.MustExec("create table t2(a int, b int)")
	tk.MustExec("insert into t1 value(1, 1), (2, 2)")
	tk.MustExec("insert into t2 value(1, 1), (2, 2)")
	tk.MustQuery("select sum(c) from (select t1.a as a, t1.a as c, length(t1.b) from t1  union select a, b, b from t2) t;").Check(testkit.Rows("5"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a bigint, b bigint, c bigint);")
	tk.MustExec("insert into t values(1, 1, 1), (2, 2, 2), (3, 3, 3);")
	rs := tk.MustQuery("select cast(count(a) as signed), a as another, a from t group by a order by cast(count(a) as signed), a limit 10;")
	rs.Check(testkit.Rows("1 1 1", "1 2 2", "1 3 3"))
}

func (s *testSuiteP1) TestIn(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t`)
	tk.MustExec(`create table t (c1 int primary key, c2 int, key c (c2));`)
	for i := 0; i <= 200; i++ {
		tk.MustExec(fmt.Sprintf("insert t values(%d, %d)", i, i))
	}
	queryStr := `select c2 from t where c1 in ('7', '10', '112', '111', '98', '106', '100', '9', '18', '17') order by c2`
	r := tk.MustQuery(queryStr)
	r.Check(testkit.Rows("7", "9", "10", "17", "18", "98", "100", "106", "111", "112"))

	queryStr = `select c2 from t where c1 in ('7a')`
	tk.MustQuery(queryStr).Check(testkit.Rows("7"))
}

func (s *testSuiteP1) TestTablePKisHandleScan(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int PRIMARY KEY AUTO_INCREMENT)")
	tk.MustExec("insert t values (),()")
	tk.MustExec("insert t values (-100),(0)")

	tests := []struct {
		sql    string
		result [][]interface{}
	}{
		{
			"select * from t",
			testkit.Rows("-100", "1", "2", "3"),
		},
		{
			"select * from t where a = 1",
			testkit.Rows("1"),
		},
		{
			"select * from t where a != 1",
			testkit.Rows("-100", "2", "3"),
		},
		{
			"select * from t where a >= '1.1'",
			testkit.Rows("2", "3"),
		},
		{
			"select * from t where a < '1.1'",
			testkit.Rows("-100", "1"),
		},
		{
			"select * from t where a > '-100.1' and a < 2",
			testkit.Rows("-100", "1"),
		},
		{
			"select * from t where a is null",
			testkit.Rows(),
		}, {
			"select * from t where a is true",
			testkit.Rows("-100", "1", "2", "3"),
		}, {
			"select * from t where a is false",
			testkit.Rows(),
		},
		{
			"select * from t where a in (1, 2)",
			testkit.Rows("1", "2"),
		},
		{
			"select * from t where a between 1 and 2",
			testkit.Rows("1", "2"),
		},
	}

	for _, tt := range tests {
		result := tk.MustQuery(tt.sql)
		result.Check(tt.result)
	}
}

func (s *testSuite8) TestIndexScan(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int unique)")
	tk.MustExec("insert t values (-1), (2), (3), (5), (6), (7), (8), (9)")
	result := tk.MustQuery("select a from t where a < 0 or (a >= 2.1 and a < 5.1) or ( a > 5.9 and a <= 7.9) or a > '8.1'")
	result.Check(testkit.Rows("-1", "3", "5", "6", "7", "9"))
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int unique)")
	tk.MustExec("insert t values (0)")
	result = tk.MustQuery("select NULL from t ")
	result.Check(testkit.Rows("<nil>"))
	// test for double read
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int unique, b int)")
	tk.MustExec("insert t values (5, 0)")
	tk.MustExec("insert t values (4, 0)")
	tk.MustExec("insert t values (3, 0)")
	tk.MustExec("insert t values (2, 0)")
	tk.MustExec("insert t values (1, 0)")
	tk.MustExec("insert t values (0, 0)")
	result = tk.MustQuery("select * from t order by a limit 3")
	result.Check(testkit.Rows("0 0", "1 0", "2 0"))
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int unique, b int)")
	tk.MustExec("insert t values (0, 1)")
	tk.MustExec("insert t values (1, 2)")
	tk.MustExec("insert t values (2, 1)")
	tk.MustExec("insert t values (3, 2)")
	tk.MustExec("insert t values (4, 1)")
	tk.MustExec("insert t values (5, 2)")
	result = tk.MustQuery("select * from t where a < 5 and b = 1 limit 2")
	result.Check(testkit.Rows("0 1", "2 1"))
	tk.MustExec("drop table if exists tab1")
	tk.MustExec("CREATE TABLE tab1(pk INTEGER PRIMARY KEY, col0 INTEGER, col1 FLOAT, col3 INTEGER, col4 FLOAT)")
	tk.MustExec("CREATE INDEX idx_tab1_0 on tab1 (col0)")
	tk.MustExec("CREATE INDEX idx_tab1_1 on tab1 (col1)")
	tk.MustExec("CREATE INDEX idx_tab1_3 on tab1 (col3)")
	tk.MustExec("CREATE INDEX idx_tab1_4 on tab1 (col4)")
	tk.MustExec("INSERT INTO tab1 VALUES(1,37,20.85,30,10.69)")
	result = tk.MustQuery("SELECT pk FROM tab1 WHERE ((col3 <= 6 OR col3 < 29 AND (col0 < 41)) OR col3 > 42) AND col1 >= 96.1 AND col3 = 30 AND col3 > 17 AND (col0 BETWEEN 36 AND 42)")
	result.Check(testkit.Rows())
	tk.MustExec("drop table if exists tab1")
	tk.MustExec("CREATE TABLE tab1(pk INTEGER PRIMARY KEY, a INTEGER, b INTEGER)")
	tk.MustExec("CREATE INDEX idx_tab1_0 on tab1 (a)")
	tk.MustExec("INSERT INTO tab1 VALUES(1,1,1)")
	tk.MustExec("INSERT INTO tab1 VALUES(2,2,1)")
	tk.MustExec("INSERT INTO tab1 VALUES(3,1,2)")
	tk.MustExec("INSERT INTO tab1 VALUES(4,2,2)")
	result = tk.MustQuery("SELECT * FROM tab1 WHERE pk <= 3 AND a = 1")
	result.Check(testkit.Rows("1 1 1", "3 1 2"))
	result = tk.MustQuery("SELECT * FROM tab1 WHERE pk <= 4 AND a = 1 AND b = 2")
	result.Check(testkit.Rows("3 1 2"))
	tk.MustExec("CREATE INDEX idx_tab1_1 on tab1 (b, a)")
	result = tk.MustQuery("SELECT pk FROM tab1 WHERE b > 1")
	result.Check(testkit.Rows("3", "4"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (a varchar(3), index(a))")
	tk.MustExec("insert t values('aaa'), ('aab')")
	result = tk.MustQuery("select * from t where a >= 'aaaa' and a < 'aabb'")
	result.Check(testkit.Rows("aab"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (a int primary key, b int, c int, index(c))")
	tk.MustExec("insert t values(1, 1, 1), (2, 2, 2), (4, 4, 4), (3, 3, 3), (5, 5, 5)")
	// Test for double read and top n.
	result = tk.MustQuery("select a from t where c >= 2 order by b desc limit 1")
	result.Check(testkit.Rows("5"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a varchar(50) primary key, b int, c int, index idx(b))")
	tk.MustExec("insert into t values('aa', 1, 1)")
	tk.MustQuery("select * from t use index(idx) where a > 'a'").Check(testkit.Rows("aa 1 1"))

	// fix issue9636
	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE `t` (a int, KEY (a))")
	result = tk.MustQuery(`SELECT * FROM (SELECT * FROM (SELECT a as d FROM t WHERE a IN ('100')) AS x WHERE x.d < "123" ) tmp_count`)
	result.Check(testkit.Rows())
}

func (s *testSuiteP1) TestIndexReverseOrder(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int primary key auto_increment, b int, index idx (b))")
	tk.MustExec("insert t (b) values (0), (1), (2), (3), (4), (5), (6), (7), (8), (9)")
	result := tk.MustQuery("select b from t order by b desc")
	result.Check(testkit.Rows("9", "8", "7", "6", "5", "4", "3", "2", "1", "0"))
	result = tk.MustQuery("select b from t where b <3 or (b >=6 and b < 8) order by b desc")
	result.Check(testkit.Rows("7", "6", "2", "1", "0"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int, b int, index idx (b, a))")
	tk.MustExec("insert t values (0, 2), (1, 2), (2, 2), (0, 1), (1, 1), (2, 1), (0, 0), (1, 0), (2, 0)")
	result = tk.MustQuery("select b, a from t order by b, a desc")
	result.Check(testkit.Rows("0 2", "0 1", "0 0", "1 2", "1 1", "1 0", "2 2", "2 1", "2 0"))
}

func (s *testSuiteP1) TestTableReverseOrder(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int primary key auto_increment, b int)")
	tk.MustExec("insert t (b) values (1), (2), (3), (4), (5), (6), (7), (8), (9)")
	result := tk.MustQuery("select b from t order by a desc")
	result.Check(testkit.Rows("9", "8", "7", "6", "5", "4", "3", "2", "1"))
	result = tk.MustQuery("select a from t where a <3 or (a >=6 and a < 8) order by a desc")
	result.Check(testkit.Rows("7", "6", "2", "1"))
}

func (s *testSuiteP1) TestDefaultNull(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int primary key auto_increment, b int default 1, c int)")
	tk.MustExec("insert t values ()")
	tk.MustQuery("select * from t").Check(testkit.Rows("1 1 <nil>"))
	tk.MustExec("update t set b = NULL where a = 1")
	tk.MustQuery("select * from t").Check(testkit.Rows("1 <nil> <nil>"))
	tk.MustExec("update t set c = 1")
	tk.MustQuery("select * from t ").Check(testkit.Rows("1 <nil> 1"))
	tk.MustExec("delete from t where a = 1")
	tk.MustExec("insert t (a) values (1)")
	tk.MustQuery("select * from t").Check(testkit.Rows("1 1 <nil>"))
}

func (s *testSuiteP1) TestUnsignedPKColumn(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int unsigned primary key, b int, c int, key idx_ba (b, c, a));")
	tk.MustExec("insert t values (1, 1, 1)")
	result := tk.MustQuery("select * from t;")
	result.Check(testkit.Rows("1 1 1"))
	tk.MustExec("update t set c=2 where a=1;")
	result = tk.MustQuery("select * from t where b=1;")
	result.Check(testkit.Rows("1 1 2"))
}

func (s *testSuiteP1) TestMultiUpdate(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`CREATE TABLE test_mu (a int primary key, b int, c int)`)
	tk.MustExec(`INSERT INTO test_mu VALUES (1, 2, 3), (4, 5, 6), (7, 8, 9)`)

	// Test INSERT ... ON DUPLICATE UPDATE set_lists.
	tk.MustExec(`INSERT INTO test_mu VALUES (1, 2, 3) ON DUPLICATE KEY UPDATE b = 3, c = b`)
	result := tk.MustQuery(`SELECT * FROM test_mu ORDER BY a`)
	result.Check(testkit.Rows(`1 3 3`, `4 5 6`, `7 8 9`))

	tk.MustExec(`INSERT INTO test_mu VALUES (1, 2, 3) ON DUPLICATE KEY UPDATE c = 2, b = c+5`)
	result = tk.MustQuery(`SELECT * FROM test_mu ORDER BY a`)
	result.Check(testkit.Rows(`1 7 2`, `4 5 6`, `7 8 9`))

	// Test UPDATE ... set_lists.
	tk.MustExec(`UPDATE test_mu SET b = 0, c = b WHERE a = 4`)
	result = tk.MustQuery(`SELECT * FROM test_mu ORDER BY a`)
	result.Check(testkit.Rows(`1 7 2`, `4 0 0`, `7 8 9`))

	tk.MustExec(`UPDATE test_mu SET c = 8, b = c WHERE a = 4`)
	result = tk.MustQuery(`SELECT * FROM test_mu ORDER BY a`)
	result.Check(testkit.Rows(`1 7 2`, `4 8 8`, `7 8 9`))

	tk.MustExec(`UPDATE test_mu SET c = b, b = c WHERE a = 7`)
	result = tk.MustQuery(`SELECT * FROM test_mu ORDER BY a`)
	result.Check(testkit.Rows(`1 7 2`, `4 8 8`, `7 8 8`))
}

func (s *testSuiteP1) TestGeneratedColumnWrite(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	_, err := tk.Exec(`CREATE TABLE test_gc_write (a int primary key auto_increment, b int, c int as (a+8) virtual)`)
	c.Assert(err.Error(), Equals, ddl.ErrGeneratedColumnRefAutoInc.GenWithStackByArgs("c").Error())
	tk.MustExec(`CREATE TABLE test_gc_write (a int primary key auto_increment, b int, c int as (b+8) virtual)`)
	tk.MustExec(`CREATE TABLE test_gc_write_1 (a int primary key, b int, c int)`)

	tests := []struct {
		stmt string
		err  int
	}{
		// Can't modify generated column by values.
		{`insert into test_gc_write (a, b, c) values (1, 1, 1)`, mysql.ErrBadGeneratedColumn},
		{`insert into test_gc_write values (1, 1, 1)`, mysql.ErrBadGeneratedColumn},
		// Can't modify generated column by select clause.
		{`insert into test_gc_write select 1, 1, 1`, mysql.ErrBadGeneratedColumn},
		// Can't modify generated column by on duplicate clause.
		{`insert into test_gc_write (a, b) values (1, 1) on duplicate key update c = 1`, mysql.ErrBadGeneratedColumn},
		// Can't modify generated column by set.
		{`insert into test_gc_write set a = 1, b = 1, c = 1`, mysql.ErrBadGeneratedColumn},
		// Can't modify generated column by update clause.
		{`update test_gc_write set c = 1`, mysql.ErrBadGeneratedColumn},
		// Can't modify generated column by multi-table update clause.
		{`update test_gc_write, test_gc_write_1 set test_gc_write.c = 1`, mysql.ErrBadGeneratedColumn},

		// Can insert without generated columns.
		{`insert into test_gc_write (a, b) values (1, 1)`, 0},
		{`insert into test_gc_write set a = 2, b = 2`, 0},
		{`insert into test_gc_write (b) select c from test_gc_write`, 0},
		// Can update without generated columns.
		{`update test_gc_write set b = 2 where a = 2`, 0},
		{`update test_gc_write t1, test_gc_write_1 t2 set t1.b = 3, t2.b = 4`, 0},

		// But now we can't do this, just as same with MySQL 5.7:
		{`insert into test_gc_write values (1, 1)`, mysql.ErrWrongValueCountOnRow},
		{`insert into test_gc_write select 1, 1`, mysql.ErrWrongValueCountOnRow},
		{`insert into test_gc_write (c) select a, b from test_gc_write`, mysql.ErrWrongValueCountOnRow},
		{`insert into test_gc_write (b, c) select a, b from test_gc_write`, mysql.ErrBadGeneratedColumn},
	}
	for _, tt := range tests {
		_, err := tk.Exec(tt.stmt)
		if tt.err != 0 {
			c.Assert(err, NotNil, Commentf("sql is `%v`", tt.stmt))
			terr := errors.Cause(err).(*terror.Error)
			c.Assert(terr.Code(), Equals, terror.ErrCode(tt.err), Commentf("sql is %v", tt.stmt))
		} else {
			c.Assert(err, IsNil)
		}
	}
}

func (s *testSuiteP2) TestToPBExpr(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a decimal(10,6), b decimal, index idx_b (b))")
	tk.MustExec("set sql_mode = ''")
	tk.MustExec("insert t values (1.1, 1.1)")
	tk.MustExec("insert t values (2.4, 2.4)")
	tk.MustExec("insert t values (3.3, 2.7)")
	result := tk.MustQuery("select * from t where a < 2.399999")
	result.Check(testkit.Rows("1.100000 1"))
	result = tk.MustQuery("select * from t where a > 1.5")
	result.Check(testkit.Rows("2.400000 2", "3.300000 3"))
	result = tk.MustQuery("select * from t where a <= 1.1")
	result.Check(testkit.Rows("1.100000 1"))
	result = tk.MustQuery("select * from t where b >= 3")
	result.Check(testkit.Rows("3.300000 3"))
	result = tk.MustQuery("select * from t where not (b = 1)")
	result.Check(testkit.Rows("2.400000 2", "3.300000 3"))
	result = tk.MustQuery("select * from t where b&1 = a|1")
	result.Check(testkit.Rows("1.100000 1"))
	result = tk.MustQuery("select * from t where b != 2 and b <=> 3")
	result.Check(testkit.Rows("3.300000 3"))
	result = tk.MustQuery("select * from t where b in (3)")
	result.Check(testkit.Rows("3.300000 3"))
	result = tk.MustQuery("select * from t where b not in (1, 2)")
	result.Check(testkit.Rows("3.300000 3"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int primary key)")
	tk.MustExec("insert t values (1)")
	tk.MustExec("insert t values (2)")
	result = tk.MustQuery("select * from t where not (a = 1)")
	result.Check(testkit.Rows("2"))
	result = tk.MustQuery("select * from t where not(not (a = 1))")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select * from t where not(a != 1 and a != 2)")
	result.Check(testkit.Rows("1", "2"))
}

func (s *testSuiteP2) TestDatumXAPI(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a decimal(10,6), b decimal, index idx_b (b))")
	tk.MustExec("set sql_mode = ''")
	tk.MustExec("insert t values (1.1, 1.1)")
	tk.MustExec("insert t values (2.2, 2.2)")
	tk.MustExec("insert t values (3.3, 2.7)")
	result := tk.MustQuery("select * from t where a > 1.5")
	result.Check(testkit.Rows("2.200000 2", "3.300000 3"))
	result = tk.MustQuery("select * from t where b > 1.5")
	result.Check(testkit.Rows("2.200000 2", "3.300000 3"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a time(3), b time, index idx_a (a))")
	tk.MustExec("insert t values ('11:11:11', '11:11:11')")
	tk.MustExec("insert t values ('11:11:12', '11:11:12')")
	tk.MustExec("insert t values ('11:11:13', '11:11:13')")
	result = tk.MustQuery("select * from t where a > '11:11:11.5'")
	result.Check(testkit.Rows("11:11:12.000 11:11:12", "11:11:13.000 11:11:13"))
	result = tk.MustQuery("select * from t where b > '11:11:11.5'")
	result.Check(testkit.Rows("11:11:12.000 11:11:12", "11:11:13.000 11:11:13"))
}

func (s *testSuiteP2) TestSQLMode(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a tinyint not null)")
	tk.MustExec("set sql_mode = 'STRICT_TRANS_TABLES'")
	_, err := tk.Exec("insert t values ()")
	c.Check(err, NotNil)

	_, err = tk.Exec("insert t values ('1000')")
	c.Check(err, NotNil)

	tk.MustExec("create table if not exists tdouble (a double(3,2))")
	_, err = tk.Exec("insert tdouble values (10.23)")
	c.Check(err, NotNil)

	tk.MustExec("set sql_mode = ''")
	tk.MustExec("insert t values ()")
	tk.MustQuery("show warnings").Check(testkit.Rows("Warning 1364 Field 'a' doesn't have a default value"))
	_, err = tk.Exec("insert t values (null)")
	c.Check(err, NotNil)
	tk.MustExec("insert ignore t values (null)")
	tk.MustQuery("show warnings").Check(testkit.Rows("Warning 1048 Column 'a' cannot be null"))
	tk.MustExec("insert t select null")
	tk.MustQuery("show warnings").Check(testkit.Rows("Warning 1048 Column 'a' cannot be null"))
	tk.MustExec("insert t values (1000)")
	tk.MustQuery("select * from t order by a").Check(testkit.Rows("0", "0", "0", "127"))

	tk.MustExec("insert tdouble values (10.23)")
	tk.MustQuery("select * from tdouble").Check(testkit.Rows("9.99"))

	tk.MustExec("set sql_mode = 'STRICT_TRANS_TABLES'")
	tk.MustExec("set @@global.sql_mode = ''")

	// Disable global variable cache, so load global session variable take effect immediate.
	s.domain.GetGlobalVarsCache().Disable()
	tk2 := testkit.NewTestKit(c, s.store)
	tk2.MustExec("use test")
	tk2.MustExec("drop table if exists t2")
	tk2.MustExec("create table t2 (a varchar(3))")
	tk2.MustExec("insert t2 values ('abcd')")
	tk2.MustQuery("select * from t2").Check(testkit.Rows("abc"))

	// session1 is still in strict mode.
	_, err = tk.Exec("insert t2 values ('abcd')")
	c.Check(err, NotNil)
	// Restore original global strict mode.
	tk.MustExec("set @@global.sql_mode = 'STRICT_TRANS_TABLES'")
}

func (s *testSuiteP2) TestTableDual(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	result := tk.MustQuery("Select 1")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("Select 1 from dual")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("Select count(*) from dual")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("Select 1 from dual where 1")
	result.Check(testkit.Rows("1"))

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int primary key)")
	tk.MustQuery("select t1.* from t t1, t t2 where t1.a=t2.a and 1=0").Check(testkit.Rows())
}

func (s *testSuiteP2) TestAdapterStatement(c *C) {
	se, err := session.CreateSession4Test(s.store)
	c.Check(err, IsNil)
	se.GetSessionVars().TxnCtx.InfoSchema = domain.GetDomain(se).InfoSchema()
	compiler := &executor.Compiler{Ctx: se}
	stmtNode, err := s.ParseOneStmt("select 1", "", "")
	c.Check(err, IsNil)
	stmt, err := compiler.Compile(context.TODO(), stmtNode)
	c.Check(err, IsNil)
	c.Check(stmt.OriginText(), Equals, "select 1")

	stmtNode, err = s.ParseOneStmt("create table test.t (a int)", "", "")
	c.Check(err, IsNil)
	stmt, err = compiler.Compile(context.TODO(), stmtNode)
	c.Check(err, IsNil)
	c.Check(stmt.OriginText(), Equals, "create table test.t (a int)")
}

func (s *testSuiteP2) TestRow(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c int, d int)")
	tk.MustExec("insert t values (1, 1)")
	tk.MustExec("insert t values (1, 3)")
	tk.MustExec("insert t values (2, 1)")
	tk.MustExec("insert t values (2, 3)")
	result := tk.MustQuery("select * from t where (c, d) < (2,2)")
	result.Check(testkit.Rows("1 1", "1 3", "2 1"))
	result = tk.MustQuery("select * from t where (1,2,3) > (3,2,1)")
	result.Check(testkit.Rows())
	result = tk.MustQuery("select * from t where row(1,2,3) > (3,2,1)")
	result.Check(testkit.Rows())
	result = tk.MustQuery("select * from t where (c, d) = (select * from t where (c,d) = (1,1))")
	result.Check(testkit.Rows("1 1"))
	result = tk.MustQuery("select * from t where (c, d) = (select * from t k where (t.c,t.d) = (c,d))")
	result.Check(testkit.Rows("1 1", "1 3", "2 1", "2 3"))
	result = tk.MustQuery("select (1, 2, 3) < (2, 3, 4)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select (2, 3, 4) <= (2, 3, 3)")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select (2, 3, 4) <= (2, 3, 4)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select (2, 3, 4) <= (2, 1, 4)")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select (2, 3, 4) >= (2, 3, 4)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select (2, 3, 4) = (2, 3, 4)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select (2, 3, 4) != (2, 3, 4)")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select row(1, 1) in (row(1, 1))")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select row(1, 0) in (row(1, 1))")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select row(1, 1) in (select 1, 1)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select row(1, 1) > row(1, 0)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select row(1, 1) > (select 1, 0)")
	result.Check(testkit.Rows("1"))
	result = tk.MustQuery("select 1 > (select 1)")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select (select 1)")
	result.Check(testkit.Rows("1"))
}

func (s *testSuiteP2) TestColumnName(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c int, d int)")
	// disable only full group by
	tk.MustExec("set sql_mode='STRICT_TRANS_TABLES'")
	rs, err := tk.Exec("select 1 + c, count(*) from t")
	c.Check(err, IsNil)
	fields := rs.Fields()
	c.Check(len(fields), Equals, 2)
	c.Check(fields[0].Column.Name.L, Equals, "1 + c")
	c.Check(fields[0].ColumnAsName.L, Equals, "1 + c")
	c.Check(fields[1].Column.Name.L, Equals, "count(*)")
	c.Check(fields[1].ColumnAsName.L, Equals, "count(*)")
	rs.Close()
	rs, err = tk.Exec("select (c) > all (select c from t) from t")
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 1)
	c.Check(fields[0].Column.Name.L, Equals, "(c) > all (select c from t)")
	c.Check(fields[0].ColumnAsName.L, Equals, "(c) > all (select c from t)")
	rs.Close()
	tk.MustExec("begin")
	tk.MustExec("insert t values(1,1)")
	rs, err = tk.Exec("select c d, d c from t")
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(len(fields), Equals, 2)
	c.Check(fields[0].Column.Name.L, Equals, "c")
	c.Check(fields[0].ColumnAsName.L, Equals, "d")
	c.Check(fields[1].Column.Name.L, Equals, "d")
	c.Check(fields[1].ColumnAsName.L, Equals, "c")
	rs.Close()
	// Test case for query a column of a table.
	// In this case, all attributes have values.
	rs, err = tk.Exec("select c as a from t as t2")
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Check(fields[0].Column.Name.L, Equals, "c")
	c.Check(fields[0].ColumnAsName.L, Equals, "a")
	c.Check(fields[0].Table.Name.L, Equals, "t")
	c.Check(fields[0].TableAsName.L, Equals, "t2")
	c.Check(fields[0].DBName.L, Equals, "test")
	rs.Close()
	// Test case for query a column wrapped with parentheses and unary plus.
	// In this case, the column name should be its original name.
	rs, err = tk.Exec("select (c), (+c), +(c), +(+(c)), ++c from t")
	c.Check(err, IsNil)
	fields = rs.Fields()
	for i := 0; i < 5; i++ {
		c.Check(fields[i].Column.Name.L, Equals, "c")
		c.Check(fields[i].ColumnAsName.L, Equals, "c")
	}
	rs.Close()

	rs, err = tk.Exec("select if(1,c,c) from t;")
	c.Check(err, IsNil)
	fields = rs.Fields()
	c.Assert(fields[0].Column.Name.L, Equals, "if(1,c,c)")
	// It's a compatibility issue. Should be empty instead.
	c.Assert(fields[0].ColumnAsName.L, Equals, "if(1,c,c)")
}

func (s *testSuiteP2) TestSelectVar(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (d int)")
	tk.MustExec("insert into t values(1), (2), (1)")
	// This behavior is different from MySQL.
	result := tk.MustQuery("select @a, @a := d+1 from t")
	result.Check(testkit.Rows("<nil> 2", "2 3", "3 2"))
	// Test for PR #10658.
	tk.MustExec("select SQL_BIG_RESULT d from t group by d")
	tk.MustExec("select SQL_SMALL_RESULT d from t group by d")
	tk.MustExec("select SQL_BUFFER_RESULT d from t group by d")
}

func (s *testSuiteP2) TestHistoryRead(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists history_read")
	tk.MustExec("create table history_read (a int)")
	tk.MustExec("insert history_read values (1)")

	// For mocktikv, safe point is not initialized, we manually insert it for snapshot to use.
	safePointName := "tikv_gc_safe_point"
	safePointValue := "20060102-15:04:05 -0700"
	safePointComment := "All versions after safe point can be accessed. (DO NOT EDIT)"
	updateSafePoint := fmt.Sprintf(`INSERT INTO mysql.tidb VALUES ('%[1]s', '%[2]s', '%[3]s')
	ON DUPLICATE KEY
	UPDATE variable_value = '%[2]s', comment = '%[3]s'`, safePointName, safePointValue, safePointComment)
	tk.MustExec(updateSafePoint)

	// Set snapshot to a time before save point will fail.
	_, err := tk.Exec("set @@tidb_snapshot = '2006-01-01 15:04:05.999999'")
	c.Assert(terror.ErrorEqual(err, variable.ErrSnapshotTooOld), IsTrue, Commentf("err %v", err))
	// SnapshotTS Is not updated if check failed.
	c.Assert(tk.Se.GetSessionVars().SnapshotTS, Equals, uint64(0))

	curVer1, _ := s.store.CurrentVersion()
	time.Sleep(time.Millisecond)
	snapshotTime := time.Now()
	time.Sleep(time.Millisecond)
	curVer2, _ := s.store.CurrentVersion()
	tk.MustExec("insert history_read values (2)")
	tk.MustQuery("select * from history_read").Check(testkit.Rows("1", "2"))
	tk.MustExec("set @@tidb_snapshot = '" + snapshotTime.Format("2006-01-02 15:04:05.999999") + "'")
	ctx := tk.Se.(sessionctx.Context)
	snapshotTS := ctx.GetSessionVars().SnapshotTS
	c.Assert(snapshotTS, Greater, curVer1.Ver)
	c.Assert(snapshotTS, Less, curVer2.Ver)
	tk.MustQuery("select * from history_read").Check(testkit.Rows("1"))
	_, err = tk.Exec("insert history_read values (2)")
	c.Assert(err, NotNil)
	_, err = tk.Exec("update history_read set a = 3 where a = 1")
	c.Assert(err, NotNil)
	_, err = tk.Exec("delete from history_read where a = 1")
	c.Assert(err, NotNil)
	tk.MustExec("set @@tidb_snapshot = ''")
	tk.MustQuery("select * from history_read").Check(testkit.Rows("1", "2"))
	tk.MustExec("insert history_read values (3)")
	tk.MustExec("update history_read set a = 4 where a = 3")
	tk.MustExec("delete from history_read where a = 1")

	time.Sleep(time.Millisecond)
	snapshotTime = time.Now()
	time.Sleep(time.Millisecond)
	tk.MustExec("alter table history_read add column b int")
	tk.MustExec("insert history_read values (8, 8), (9, 9)")
	tk.MustQuery("select * from history_read order by a").Check(testkit.Rows("2 <nil>", "4 <nil>", "8 8", "9 9"))
	tk.MustExec("set @@tidb_snapshot = '" + snapshotTime.Format("2006-01-02 15:04:05.999999") + "'")
	tk.MustQuery("select * from history_read order by a").Check(testkit.Rows("2", "4"))
	tsoStr := strconv.FormatUint(oracle.EncodeTSO(snapshotTime.UnixNano()/int64(time.Millisecond)), 10)

	tk.MustExec("set @@tidb_snapshot = '" + tsoStr + "'")
	tk.MustQuery("select * from history_read order by a").Check(testkit.Rows("2", "4"))

	tk.MustExec("set @@tidb_snapshot = ''")
	tk.MustQuery("select * from history_read order by a").Check(testkit.Rows("2 <nil>", "4 <nil>", "8 8", "9 9"))
}

func (s *testSuite2) TestLowResolutionTSORead(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("set @@autocommit=1")
	tk.MustExec("use test")
	tk.MustExec("drop table if exists low_resolution_tso")
	tk.MustExec("create table low_resolution_tso(a int)")
	tk.MustExec("insert low_resolution_tso values (1)")

	// enable low resolution tso
	c.Assert(tk.Se.GetSessionVars().LowResolutionTSO, IsFalse)
	tk.Exec("set @@tidb_low_resolution_tso = 'on'")
	c.Assert(tk.Se.GetSessionVars().LowResolutionTSO, IsTrue)

	time.Sleep(3 * time.Second)
	tk.MustQuery("select * from low_resolution_tso").Check(testkit.Rows("1"))
	_, err := tk.Exec("update low_resolution_tso set a = 2")
	c.Assert(err, NotNil)
	tk.MustExec("set @@tidb_low_resolution_tso = 'off'")
	tk.MustExec("update low_resolution_tso set a = 2")
	tk.MustQuery("select * from low_resolution_tso").Check(testkit.Rows("2"))
}

func (s *testSuite) TestScanControlSelection(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int primary key, b int, c int, index idx_b(b))")
	tk.MustExec("insert into t values (1, 1, 1), (2, 1, 1), (3, 1, 2), (4, 2, 3)")
	tk.MustQuery("select (select count(1) k from t s where s.b = t1.c) from t t1").Sort().Check(testkit.Rows("0", "1", "3", "3"))
}

func (s *testSuite) TestSimpleDAG(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int primary key, b int, c int)")
	tk.MustExec("insert into t values (1, 1, 1), (2, 1, 1), (3, 1, 2), (4, 2, 3)")
	tk.MustQuery("select a from t").Check(testkit.Rows("1", "2", "3", "4"))
	tk.MustQuery("select * from t where a = 4").Check(testkit.Rows("4 2 3"))
	tk.MustQuery("select a from t limit 1").Check(testkit.Rows("1"))
	tk.MustQuery("select a from t order by a desc").Check(testkit.Rows("4", "3", "2", "1"))
	tk.MustQuery("select a from t order by a desc limit 1").Check(testkit.Rows("4"))
	tk.MustQuery("select a from t order by b desc limit 1").Check(testkit.Rows("4"))
	tk.MustQuery("select a from t where a < 3").Check(testkit.Rows("1", "2"))
	tk.MustQuery("select a from t where b > 1").Check(testkit.Rows("4"))
	tk.MustQuery("select a from t where b > 1 and a < 3").Check(testkit.Rows())
	tk.MustQuery("select count(*) from t where b > 1 and a < 3").Check(testkit.Rows("0"))
	tk.MustQuery("select count(*) from t").Check(testkit.Rows("4"))
	tk.MustQuery("select count(*), c from t group by c order by c").Check(testkit.Rows("2 1", "1 2", "1 3"))
	tk.MustQuery("select sum(c) as s from t group by b order by s").Check(testkit.Rows("3", "4"))
	tk.MustQuery("select avg(a) as s from t group by b order by s").Check(testkit.Rows("2.0000", "4.0000"))
	tk.MustQuery("select sum(distinct c) from t group by b").Check(testkit.Rows("3", "3"))

	tk.MustExec("create index i on t(c,b)")
	tk.MustQuery("select a from t where c = 1").Check(testkit.Rows("1", "2"))
	tk.MustQuery("select a from t where c = 1 and a < 2").Check(testkit.Rows("1"))
	tk.MustQuery("select a from t where c = 1 order by a limit 1").Check(testkit.Rows("1"))
	tk.MustQuery("select count(*) from t where c = 1 ").Check(testkit.Rows("2"))
	tk.MustExec("create index i1 on t(b)")
	tk.MustQuery("select c from t where b = 2").Check(testkit.Rows("3"))
	tk.MustQuery("select * from t where b = 2").Check(testkit.Rows("4 2 3"))
	tk.MustQuery("select count(*) from t where b = 1").Check(testkit.Rows("3"))
	tk.MustQuery("select * from t where b = 1 and a > 1 limit 1").Check(testkit.Rows("2 1 1"))

	// Test time push down.
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (id int, c1 datetime);")
	tk.MustExec("insert into t values (1, '2015-06-07 12:12:12')")
	tk.MustQuery("select id from t where c1 = '2015-06-07 12:12:12'").Check(testkit.Rows("1"))
}

func (s *testSuite) TestTimestampTimeZone(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (ts timestamp)")
	tk.MustExec("set time_zone = '+00:00'")
	tk.MustExec("insert into t values ('2017-04-27 22:40:42')")
	// The timestamp will get different value if time_zone session variable changes.
	tests := []struct {
		timezone string
		expect   string
	}{
		{"+10:00", "2017-04-28 08:40:42"},
		{"-6:00", "2017-04-27 16:40:42"},
	}
	for _, tt := range tests {
		tk.MustExec(fmt.Sprintf("set time_zone = '%s'", tt.timezone))
		tk.MustQuery("select * from t").Check(testkit.Rows(tt.expect))
	}

	// For issue https://github.com/pingcap/tidb/issues/3467
	tk.MustExec("drop table if exists t1")
	tk.MustExec(`CREATE TABLE t1 (
 	      id bigint(20) NOT NULL AUTO_INCREMENT,
 	      uid int(11) DEFAULT NULL,
 	      datetime timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
 	      ip varchar(128) DEFAULT NULL,
 	    PRIMARY KEY (id),
 	      KEY i_datetime (datetime),
 	      KEY i_userid (uid)
 	    );`)
	tk.MustExec(`INSERT INTO t1 VALUES (123381351,1734,"2014-03-31 08:57:10","127.0.0.1");`)
	r := tk.MustQuery("select datetime from t1;") // Cover TableReaderExec
	r.Check(testkit.Rows("2014-03-31 08:57:10"))
	r = tk.MustQuery("select datetime from t1 where datetime='2014-03-31 08:57:10';")
	r.Check(testkit.Rows("2014-03-31 08:57:10")) // Cover IndexReaderExec
	r = tk.MustQuery("select * from t1 where datetime='2014-03-31 08:57:10';")
	r.Check(testkit.Rows("123381351 1734 2014-03-31 08:57:10 127.0.0.1")) // Cover IndexLookupExec

	// For issue https://github.com/pingcap/tidb/issues/3485
	tk.MustExec("set time_zone = 'Asia/Shanghai'")
	tk.MustExec("drop table if exists t1")
	tk.MustExec(`CREATE TABLE t1 (
	    id bigint(20) NOT NULL AUTO_INCREMENT,
	    datetime timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
	    PRIMARY KEY (id)
	  );`)
	tk.MustExec(`INSERT INTO t1 VALUES (123381351,"2014-03-31 08:57:10");`)
	r = tk.MustQuery(`select * from t1 where datetime="2014-03-31 08:57:10";`)
	r.Check(testkit.Rows("123381351 2014-03-31 08:57:10"))
	tk.MustExec(`alter table t1 add key i_datetime (datetime);`)
	r = tk.MustQuery(`select * from t1 where datetime="2014-03-31 08:57:10";`)
	r.Check(testkit.Rows("123381351 2014-03-31 08:57:10"))
	r = tk.MustQuery(`select * from t1;`)
	r.Check(testkit.Rows("123381351 2014-03-31 08:57:10"))
	r = tk.MustQuery("select datetime from t1 where datetime='2014-03-31 08:57:10';")
	r.Check(testkit.Rows("2014-03-31 08:57:10"))
}

func (s *testSuite) TestTimestampDefaultValueTimeZone(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("set time_zone = '+08:00'")
	tk.MustExec(`create table t (a int, b timestamp default "2019-01-17 14:46:14")`)
	tk.MustExec("insert into t set a=1")
	r := tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '2019-01-17 14:46:14'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))
	tk.MustExec("set time_zone = '+00:00'")
	tk.MustExec("insert into t set a=2")
	r = tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '2019-01-17 06:46:14'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))
	r = tk.MustQuery(`select a,b from t order by a`)
	r.Check(testkit.Rows("1 2019-01-17 06:46:14", "2 2019-01-17 06:46:14"))
	// Test the column's version is greater than ColumnInfoVersion1.
	sctx := tk.Se.(sessionctx.Context)
	is := domain.GetDomain(sctx).InfoSchema()
	c.Assert(is, NotNil)
	tb, err := is.TableByName(model.NewCIStr("test"), model.NewCIStr("t"))
	c.Assert(err, IsNil)
	tb.Cols()[1].Version = model.ColumnInfoVersion1 + 1
	tk.MustExec("insert into t set a=3")
	r = tk.MustQuery(`select a,b from t order by a`)
	r.Check(testkit.Rows("1 2019-01-17 06:46:14", "2 2019-01-17 06:46:14", "3 2019-01-17 06:46:14"))
	tk.MustExec("delete from t where a=3")
	// Change time zone back.
	tk.MustExec("set time_zone = '+08:00'")
	r = tk.MustQuery(`select a,b from t order by a`)
	r.Check(testkit.Rows("1 2019-01-17 14:46:14", "2 2019-01-17 14:46:14"))
	tk.MustExec("set time_zone = '-08:00'")
	r = tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '2019-01-16 22:46:14'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))

	// test zero default value in multiple time zone.
	defer tk.MustExec(fmt.Sprintf("set @@sql_mode='%s'", tk.MustQuery("select @@sql_mode").Rows()[0][0]))
	tk.MustExec("set @@sql_mode='STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION';")
	tk.MustExec("drop table if exists t")
	tk.MustExec("set time_zone = '+08:00'")
	tk.MustExec(`create table t (a int, b timestamp default "0000-00-00 00")`)
	tk.MustExec("insert into t set a=1")
	r = tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '0000-00-00 00:00:00'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))
	tk.MustExec("set time_zone = '+00:00'")
	tk.MustExec("insert into t set a=2")
	r = tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '0000-00-00 00:00:00'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))
	tk.MustExec("set time_zone = '-08:00'")
	tk.MustExec("insert into t set a=3")
	r = tk.MustQuery(`show create table t`)
	r.Check(testkit.Rows("t CREATE TABLE `t` (\n" + "  `a` int(11) DEFAULT NULL,\n" + "  `b` timestamp DEFAULT '0000-00-00 00:00:00'\n" + ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"))
	r = tk.MustQuery(`select a,b from t order by a`)
	r.Check(testkit.Rows("1 0000-00-00 00:00:00", "2 0000-00-00 00:00:00", "3 0000-00-00 00:00:00"))

	// test add timestamp column default current_timestamp.
	tk.MustExec(`drop table if exists t`)
	tk.MustExec(`set time_zone = 'Asia/Shanghai'`)
	tk.MustExec(`create table t (a int)`)
	tk.MustExec(`insert into t set a=1`)
	tk.MustExec(`alter table t add column b timestamp not null default current_timestamp;`)
	timeIn8 := tk.MustQuery("select b from t").Rows()[0][0]
	tk.MustExec(`set time_zone = '+00:00'`)
	timeIn0 := tk.MustQuery("select b from t").Rows()[0][0]
	c.Assert(timeIn8 != timeIn0, IsTrue, Commentf("%v == %v", timeIn8, timeIn0))
	datumTimeIn8, err := expression.GetTimeValue(tk.Se, timeIn8, mysql.TypeTimestamp, 0)
	c.Assert(err, IsNil)
	tIn8To0 := datumTimeIn8.GetMysqlTime()
	timeZoneIn8, err := time.LoadLocation("Asia/Shanghai")
	c.Assert(err, IsNil)
	err = tIn8To0.ConvertTimeZone(timeZoneIn8, time.UTC)
	c.Assert(err, IsNil)
	c.Assert(timeIn0 == tIn8To0.String(), IsTrue, Commentf("%v != %v", timeIn0, tIn8To0.String()))

	// test add index.
	tk.MustExec(`alter table t add index(b);`)

	tk.MustExec(`set time_zone = '+05:00'`)

}

func (s *testSuite) TestTiDBCurrentTS(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustQuery("select @@tidb_current_ts").Check(testkit.Rows("0"))
	tk.MustExec("begin")
	rows := tk.MustQuery("select @@tidb_current_ts").Rows()
	tsStr := rows[0][0].(string)
	txn, err := tk.Se.Txn(true)
	c.Assert(err, IsNil)
	c.Assert(tsStr, Equals, fmt.Sprintf("%d", txn.StartTS()))
	tk.MustExec("begin")
	rows = tk.MustQuery("select @@tidb_current_ts").Rows()
	newTsStr := rows[0][0].(string)
	txn, err = tk.Se.Txn(true)
	c.Assert(err, IsNil)
	c.Assert(newTsStr, Equals, fmt.Sprintf("%d", txn.StartTS()))
	c.Assert(newTsStr, Not(Equals), tsStr)
	tk.MustExec("commit")
	tk.MustQuery("select @@tidb_current_ts").Check(testkit.Rows("0"))

	_, err = tk.Exec("set @@tidb_current_ts = '1'")
	c.Assert(terror.ErrorEqual(err, variable.ErrReadOnly), IsTrue, Commentf("err %v", err))
}

func (s *testSuite) TestEmptyEnum(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (e enum('Y', 'N'))")
	tk.MustExec("set sql_mode='STRICT_TRANS_TABLES'")
	_, err := tk.Exec("insert into t values (0)")
	c.Assert(terror.ErrorEqual(err, table.ErrTruncatedWrongValueForField), IsTrue, Commentf("err %v", err))
	_, err = tk.Exec("insert into t values ('abc')")
	c.Assert(terror.ErrorEqual(err, table.ErrTruncatedWrongValueForField), IsTrue, Commentf("err %v", err))

	tk.MustExec("set sql_mode=''")
	tk.MustExec("insert into t values (0)")
	tk.MustQuery("select * from t").Check(testkit.Rows(""))
	tk.MustExec("insert into t values ('abc')")
	tk.MustQuery("select * from t").Check(testkit.Rows("", ""))
	tk.MustExec("insert into t values (null)")
	tk.MustQuery("select * from t").Check(testkit.Rows("", "", "<nil>"))
}

// TestIssue4024 This tests https://github.com/pingcap/tidb/issues/4024
func (s *testSuite) TestIssue4024(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("create database test2")
	tk.MustExec("use test2")
	tk.MustExec("create table t(a int)")
	tk.MustExec("insert into t values(1)")
	tk.MustExec("use test")
	tk.MustExec("create table t(a int)")
	tk.MustExec("insert into t values(1)")
	tk.MustExec("update t, test2.t set test2.t.a=2")
	tk.MustQuery("select * from t").Check(testkit.Rows("1"))
	tk.MustQuery("select * from test2.t").Check(testkit.Rows("2"))
	tk.MustExec("update test.t, test2.t set test.t.a=3")
	tk.MustQuery("select * from t").Check(testkit.Rows("3"))
	tk.MustQuery("select * from test2.t").Check(testkit.Rows("2"))
}

const (
	checkRequestOff = iota
	checkRequestSyncLog
	checkDDLAddIndexPriority
)

type checkRequestClient struct {
	tikv.Client
	priority       pb.CommandPri
	lowPriorityCnt uint32
	mu             struct {
		sync.RWMutex
		checkFlags uint32
		syncLog    bool
	}
}

func (c *checkRequestClient) setCheckPriority(priority pb.CommandPri) {
	atomic.StoreInt32((*int32)(&c.priority), int32(priority))
}

func (c *checkRequestClient) getCheckPriority() pb.CommandPri {
	return (pb.CommandPri)(atomic.LoadInt32((*int32)(&c.priority)))
}

func (c *checkRequestClient) SendRequest(ctx context.Context, addr string, req *tikvrpc.Request, timeout time.Duration) (*tikvrpc.Response, error) {
	resp, err := c.Client.SendRequest(ctx, addr, req, timeout)
	c.mu.RLock()
	checkFlags := c.mu.checkFlags
	c.mu.RUnlock()
	if checkFlags == checkRequestSyncLog {
		switch req.Type {
		case tikvrpc.CmdPrewrite, tikvrpc.CmdCommit:
			c.mu.RLock()
			syncLog := c.mu.syncLog
			c.mu.RUnlock()
			if syncLog != req.SyncLog {
				return nil, errors.New("fail to set sync log")
			}
		}
	} else if checkFlags == checkDDLAddIndexPriority {
		if req.Type == tikvrpc.CmdScan {
			if c.getCheckPriority() != req.Priority {
				return nil, errors.New("fail to set priority")
			}
		} else if req.Type == tikvrpc.CmdPrewrite {
			if c.getCheckPriority() == pb.CommandPri_Low {
				atomic.AddUint32(&c.lowPriorityCnt, 1)
			}
		}
	}
	return resp, err
}

type testSuite1 struct {
	store kv.Storage
	dom   *domain.Domain
	cli   *checkRequestClient
}

func (s *testSuite1) SetUpSuite(c *C) {
	cli := &checkRequestClient{}
	hijackClient := func(c tikv.Client) tikv.Client {
		cli.Client = c
		return cli
	}
	s.cli = cli

	var err error
	s.store, err = mockstore.NewMockTikvStore(
		mockstore.WithHijackClient(hijackClient),
	)
	c.Assert(err, IsNil)
	session.SetStatsLease(0)
	s.dom, err = session.BootstrapSession(s.store)
	c.Assert(err, IsNil)
}

func (s *testSuite1) TearDownSuite(c *C) {
	s.dom.Close()
	s.store.Close()
}

func (s *testSuite1) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

func (s *testSuite2) TestAddIndexPriority(c *C) {
	cli := &checkRequestClient{}
	hijackClient := func(c tikv.Client) tikv.Client {
		cli.Client = c
		return cli
	}

	store, err := mockstore.NewMockTikvStore(
		mockstore.WithHijackClient(hijackClient),
	)
	c.Assert(err, IsNil)
	dom, err := session.BootstrapSession(store)
	c.Assert(err, IsNil)
	defer func() {
		dom.Close()
		store.Close()
	}()

	tk := testkit.NewTestKit(c, store)
	tk.MustExec("use test")
	tk.MustExec("create table t1 (id int, v int)")

	// Insert some data to make sure plan build IndexLookup for t1.
	for i := 0; i < 10; i++ {
		tk.MustExec(fmt.Sprintf("insert into t1 values (%d, %d)", i, i))
	}

	cli.mu.Lock()
	cli.mu.checkFlags = checkDDLAddIndexPriority
	cli.mu.Unlock()

	cli.setCheckPriority(pb.CommandPri_Low)
	tk.MustExec("alter table t1 add index t1_index (id);")

	c.Assert(atomic.LoadUint32(&cli.lowPriorityCnt) > 0, IsTrue)

	cli.mu.Lock()
	cli.mu.checkFlags = checkRequestOff
	cli.mu.Unlock()

	tk.MustExec("alter table t1 drop index t1_index;")
	tk.MustExec("SET SESSION tidb_ddl_reorg_priority = 'PRIORITY_NORMAL'")

	cli.mu.Lock()
	cli.mu.checkFlags = checkDDLAddIndexPriority
	cli.mu.Unlock()

	cli.setCheckPriority(pb.CommandPri_Normal)
	tk.MustExec("alter table t1 add index t1_index (id);")

	cli.mu.Lock()
	cli.mu.checkFlags = checkRequestOff
	cli.mu.Unlock()

	tk.MustExec("alter table t1 drop index t1_index;")
	tk.MustExec("SET SESSION tidb_ddl_reorg_priority = 'PRIORITY_HIGH'")

	cli.mu.Lock()
	cli.mu.checkFlags = checkDDLAddIndexPriority
	cli.mu.Unlock()

	cli.setCheckPriority(pb.CommandPri_High)
	tk.MustExec("alter table t1 add index t1_index (id);")

	cli.mu.Lock()
	cli.mu.checkFlags = checkRequestOff
	cli.mu.Unlock()
}

func (s *testSuite1) TestAlterTableComment(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t_1")
	tk.MustExec("create table t_1 (c1 int, c2 int, c3 int default 1, index (c1)) comment = 'test table';")
	tk.MustExec("alter table `t_1` comment 'this is table comment';")
	result := tk.MustQuery("select table_comment from information_schema.tables where table_name = 't_1';")
	result.Check(testkit.Rows("this is table comment"))
	tk.MustExec("alter table `t_1` comment 'table t comment';")
	result = tk.MustQuery("select table_comment from information_schema.tables where table_name = 't_1';")
	result.Check(testkit.Rows("table t comment"))
}

func (s *testSuite) TestTimezonePushDown(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("create table t (ts timestamp)")
	defer tk.MustExec("drop table t")
	tk.MustExec(`insert into t values ("2018-09-13 10:02:06")`)

	systemTZ := timeutil.SystemLocation()
	c.Assert(systemTZ.String(), Not(Equals), "System")
	c.Assert(systemTZ.String(), Not(Equals), "Local")
	ctx := context.Background()
	count := 0
	ctx1 := context.WithValue(ctx, "CheckSelectRequestHook", func(req *kv.Request) {
		count += 1
		dagReq := new(tipb.DAGRequest)
		err := proto.Unmarshal(req.Data, dagReq)
		c.Assert(err, IsNil)
		c.Assert(dagReq.GetTimeZoneName(), Equals, systemTZ.String())
	})
	tk.Se.Execute(ctx1, `select * from t where ts = "2018-09-13 10:02:06"`)

	tk.MustExec(`set time_zone="System"`)
	tk.Se.Execute(ctx1, `select * from t where ts = "2018-09-13 10:02:06"`)

	c.Assert(count, Equals, 2) // Make sure the hook function is called.
}

func (s *testSuite) TestNotFillCacheFlag(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("create table t (id int primary key)")
	defer tk.MustExec("drop table t")
	tk.MustExec("insert into t values (1)")

	tests := []struct {
		sql    string
		expect bool
	}{
		{"select SQL_NO_CACHE * from t", true},
		{"select SQL_CACHE * from t", false},
		{"select * from t", false},
	}
	count := 0
	ctx := context.Background()
	for _, test := range tests {
		ctx1 := context.WithValue(ctx, "CheckSelectRequestHook", func(req *kv.Request) {
			count++
			if req.NotFillCache != test.expect {
				c.Errorf("sql=%s, expect=%v, get=%v", test.sql, test.expect, req.NotFillCache)
			}
		})
		rs, err := tk.Se.Execute(ctx1, test.sql)
		c.Assert(err, IsNil)
		tk.ResultSetToResult(rs[0], Commentf("sql: %v", test.sql))
	}
	c.Assert(count, Equals, len(tests)) // Make sure the hook function is called.
}

func (s *testSuite1) TestSyncLog(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	cli := s.cli
	cli.mu.Lock()
	cli.mu.checkFlags = checkRequestSyncLog
	cli.mu.syncLog = true
	cli.mu.Unlock()
	tk.MustExec("create table t (id int primary key)")
	cli.mu.Lock()
	cli.mu.syncLog = false
	cli.mu.Unlock()
	tk.MustExec("insert into t values (1)")

	cli.mu.Lock()
	cli.mu.checkFlags = checkRequestOff
	cli.mu.Unlock()
}

func (s *testSuite) TestHandleTransfer(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("create table t(a int, index idx(a))")
	tk.MustExec("insert into t values(1), (2), (4)")
	tk.MustExec("begin")
	tk.MustExec("update t set a = 3 where a = 4")
	// test table scan read whose result need handle.
	tk.MustQuery("select * from t ignore index(idx)").Check(testkit.Rows("1", "2", "3"))
	tk.MustExec("insert into t values(4)")
	// test single read whose result need handle
	tk.MustQuery("select * from t use index(idx)").Check(testkit.Rows("1", "2", "3", "4"))
	tk.MustQuery("select * from t use index(idx) order by a desc").Check(testkit.Rows("4", "3", "2", "1"))
	tk.MustExec("update t set a = 5 where a = 3")
	tk.MustQuery("select * from t use index(idx)").Check(testkit.Rows("1", "2", "4", "5"))
	tk.MustExec("commit")

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b int, index idx(a))")
	tk.MustExec("insert into t values(3, 3), (1, 1), (2, 2)")
	// Second test double read.
	tk.MustQuery("select * from t use index(idx) order by a").Check(testkit.Rows("1 1", "2 2", "3 3"))
}

func (s *testSuite) TestBit(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(2))")
	tk.MustExec("insert into t values (0), (1), (2), (3)")
	_, err := tk.Exec("insert into t values (4)")
	c.Assert(err, NotNil)
	_, err = tk.Exec("insert into t values ('a')")
	c.Assert(err, NotNil)
	r, err := tk.Exec("select * from t where c1 = 2")
	c.Assert(err, IsNil)
	req := r.NewChunk()
	err = r.Next(context.Background(), req)
	c.Assert(err, IsNil)
	c.Assert(types.BinaryLiteral(req.GetRow(0).GetBytes(0)), DeepEquals, types.NewBinaryLiteralFromUint(2, -1))
	r.Close()

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(31))")
	tk.MustExec("insert into t values (0x7fffffff)")
	_, err = tk.Exec("insert into t values (0x80000000)")
	c.Assert(err, NotNil)
	_, err = tk.Exec("insert into t values (0xffffffff)")
	c.Assert(err, NotNil)
	tk.MustExec("insert into t values ('123')")
	tk.MustExec("insert into t values ('1234')")
	_, err = tk.Exec("insert into t values ('12345)")
	c.Assert(err, NotNil)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(62))")
	tk.MustExec("insert into t values ('12345678')")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(61))")
	_, err = tk.Exec("insert into t values ('12345678')")
	c.Assert(err, NotNil)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(32))")
	tk.MustExec("insert into t values (0x7fffffff)")
	tk.MustExec("insert into t values (0xffffffff)")
	_, err = tk.Exec("insert into t values (0x1ffffffff)")
	c.Assert(err, NotNil)
	tk.MustExec("insert into t values ('1234')")
	_, err = tk.Exec("insert into t values ('12345')")
	c.Assert(err, NotNil)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c1 bit(64))")
	tk.MustExec("insert into t values (0xffffffffffffffff)")
	tk.MustExec("insert into t values ('12345678')")
	_, err = tk.Exec("insert into t values ('123456789')")
	c.Assert(err, NotNil)
}

func (s *testSuite) TestEnum(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c enum('a', 'b', 'c'))")
	tk.MustExec("insert into t values ('a'), (2), ('c')")
	tk.MustQuery("select * from t where c = 'a'").Check(testkit.Rows("a"))

	tk.MustQuery("select c + 1 from t where c = 2").Check(testkit.Rows("3"))

	tk.MustExec("delete from t")
	tk.MustExec("insert into t values ()")
	tk.MustExec("insert into t values (null), ('1')")
	tk.MustQuery("select c + 1 from t where c = 1").Check(testkit.Rows("2"))
}

func (s *testSuite) TestSet(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (c set('a', 'b', 'c'))")
	tk.MustExec("insert into t values ('a'), (2), ('c'), ('a,b'), ('b,a')")
	tk.MustQuery("select * from t where c = 'a'").Check(testkit.Rows("a"))

	tk.MustQuery("select * from t where c = 'a,b'").Check(testkit.Rows("a,b", "a,b"))

	tk.MustQuery("select c + 1 from t where c = 2").Check(testkit.Rows("3"))

	tk.MustExec("delete from t")
	tk.MustExec("insert into t values ()")
	tk.MustExec("insert into t values (null), ('1')")
	tk.MustQuery("select c + 1 from t where c = 1").Check(testkit.Rows("2"))
}

func (s *testSuite) TestSubqueryInValues(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (id int, name varchar(20))")
	tk.MustExec("drop table if exists t1")
	tk.MustExec("create table t1 (gid int)")

	tk.MustExec("insert into t1 (gid) value (1)")
	tk.MustExec("insert into t (id, name) value ((select gid from t1) ,'asd')")
	tk.MustQuery("select * from t").Check(testkit.Rows("1 asd"))
}

func (s *testSuite) TestEnhancedRangeAccess(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t (a int primary key, b int)")
	tk.MustExec("insert into t values(1, 2), (2, 1)")
	tk.MustQuery("select * from t where (a = 1 and b = 2) or (a = 2 and b = 1)").Check(testkit.Rows("1 2", "2 1"))
	tk.MustQuery("select * from t where (a = 1 and b = 1) or (a = 2 and b = 2)").Check(nil)
}

// TestMaxInt64Handle Issue #4810
func (s *testSuite) TestMaxInt64Handle(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(id bigint, PRIMARY KEY (id))")
	tk.MustExec("insert into t values(9223372036854775807)")
	tk.MustExec("select * from t where id = 9223372036854775807")
	tk.MustQuery("select * from t where id = 9223372036854775807;").Check(testkit.Rows("9223372036854775807"))
	tk.MustQuery("select * from t").Check(testkit.Rows("9223372036854775807"))
	_, err := tk.Exec("insert into t values(9223372036854775807)")
	c.Assert(err, NotNil)
	tk.MustExec("delete from t where id = 9223372036854775807")
	tk.MustQuery("select * from t").Check(nil)
}

func (s *testSuite) TestTableScanWithPointRanges(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(id int, PRIMARY KEY (id))")
	tk.MustExec("insert into t values(1), (5), (10)")
	tk.MustQuery("select * from t where id in(1, 2, 10)").Check(testkit.Rows("1", "10"))
}

func (s *testSuite) TestUnsignedPk(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)

	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(id bigint unsigned primary key)")
	var num1, num2 uint64 = math.MaxInt64 + 1, math.MaxInt64 + 2
	tk.MustExec(fmt.Sprintf("insert into t values(%v), (%v), (1), (2)", num1, num2))
	num1Str := strconv.FormatUint(num1, 10)
	num2Str := strconv.FormatUint(num2, 10)
	tk.MustQuery("select * from t order by id").Check(testkit.Rows("1", "2", num1Str, num2Str))
	tk.MustQuery("select * from t where id not in (2)").Check(testkit.Rows(num1Str, num2Str, "1"))
	tk.MustExec("drop table t")
	tk.MustExec("create table t(a bigint unsigned primary key, b int, index idx(b))")
	tk.MustExec("insert into t values(9223372036854775808, 1), (1, 1)")
	tk.MustQuery("select * from t use index(idx) where b = 1 and a < 2").Check(testkit.Rows("1 1"))
	tk.MustQuery("select * from t use index(idx) where b = 1 order by b, a").Check(testkit.Rows("1 1", "9223372036854775808 1"))
}

func (s *testSuite) TestIssue5666(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("set @@profiling=1")
	tk.MustQuery("SELECT QUERY_ID, SUM(DURATION) AS SUM_DURATION FROM INFORMATION_SCHEMA.PROFILING GROUP BY QUERY_ID;").Check(testkit.Rows("0 0"))
}

func (s *testSuite) TestIssue5341(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("drop table if exists test.t")
	tk.MustExec("create table test.t(a char)")
	tk.MustExec("insert into test.t value('a')")
	tk.MustQuery("select * from test.t where a < 1 order by a limit 0;").Check(testkit.Rows())
}

func (s *testSuite) TestContainDotColumn(c *C) {
	tk := testkit.NewTestKit(c, s.store)

	tk.MustExec("use test")
	tk.MustExec("drop table if exists test.t1")
	tk.MustExec("create table test.t1(t1.a char)")
	tk.MustExec("drop table if exists t2")
	tk.MustExec("create table t2(a char, t2.b int)")

	tk.MustExec("drop table if exists t3")
	_, err := tk.Exec("create table t3(s.a char);")
	terr := errors.Cause(err).(*terror.Error)
	c.Assert(terr.Code(), Equals, terror.ErrCode(mysql.ErrWrongTableName))
}

func (s *testSuite) TestLimit(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec(`use test;`)
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t(a bigint, b bigint);`)
	tk.MustExec(`insert into t values(1, 1), (2, 2), (3, 3), (4, 4), (5, 5), (6, 6);`)
	tk.MustQuery(`select * from t order by a limit 1, 1;`).Check(testkit.Rows(
		"2 2",
	))
	tk.MustQuery(`select * from t order by a limit 1, 2;`).Check(testkit.Rows(
		"2 2",
		"3 3",
	))
	tk.MustQuery(`select * from t order by a limit 1, 3;`).Check(testkit.Rows(
		"2 2",
		"3 3",
		"4 4",
	))
	tk.MustQuery(`select * from t order by a limit 1, 4;`).Check(testkit.Rows(
		"2 2",
		"3 3",
		"4 4",
		"5 5",
	))
	tk.MustExec(`set @@tidb_init_chunk_size=2;`)
	tk.MustQuery(`select * from t order by a limit 2, 1;`).Check(testkit.Rows(
		"3 3",
	))
	tk.MustQuery(`select * from t order by a limit 2, 2;`).Check(testkit.Rows(
		"3 3",
		"4 4",
	))
	tk.MustQuery(`select * from t order by a limit 2, 3;`).Check(testkit.Rows(
		"3 3",
		"4 4",
		"5 5",
	))
	tk.MustQuery(`select * from t order by a limit 2, 4;`).Check(testkit.Rows(
		"3 3",
		"4 4",
		"5 5",
		"6 6",
	))
}

func (s *testSuite3) TestYearTypeDeleteIndex(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a YEAR, PRIMARY KEY(a));")
	tk.MustExec("insert into t set a = '2151';")
	tk.MustExec("delete from t;")
}

func (s *testSuite3) TestIndexJoinTableDualPanic(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists a")
	tk.MustExec("create table a (f1 int, f2 varchar(32), primary key (f1))")
	tk.MustExec("insert into a (f1,f2) values (1,'a'), (2,'b'), (3,'c')")
	tk.MustQuery("select a.* from a inner join (select 1 as k1,'k2-1' as k2) as k on a.f1=k.k1;").
		Check(testkit.Rows("1 a"))
}

func (s *testSuite3) TestUnionAutoSignedCast(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t1,t2")
	tk.MustExec("create table t1 (id int, i int, b bigint, d double, dd decimal)")
	tk.MustExec("create table t2 (id int, i int unsigned, b bigint unsigned, d double unsigned, dd decimal unsigned)")
	tk.MustExec("insert into t1 values(1, -1, -1, -1.1, -1)")
	tk.MustExec("insert into t2 values(2, 1, 1, 1.1, 1)")
	tk.MustQuery("select * from t1 union select * from t2 order by id").
		Check(testkit.Rows("1 -1 -1 -1.1 -1", "2 1 1 1.1 1"))
	tk.MustQuery("select id, i, b, d, dd from t2 union select id, i, b, d, dd from t1 order by id").
		Check(testkit.Rows("1 0 0 0 -1", "2 1 1 1.1 1"))
	tk.MustQuery("select id, i from t2 union select id, cast(i as unsigned int) from t1 order by id").
		Check(testkit.Rows("1 18446744073709551615", "2 1"))
	tk.MustQuery("select dd from t2 union all select dd from t2").
		Check(testkit.Rows("1", "1"))

	tk.MustExec("drop table if exists t3,t4")
	tk.MustExec("create table t3 (id int, v int)")
	tk.MustExec("create table t4 (id int, v double unsigned)")
	tk.MustExec("insert into t3 values (1, -1)")
	tk.MustExec("insert into t4 values (2, 1)")
	tk.MustQuery("select id, v from t3 union select id, v from t4 order by id").
		Check(testkit.Rows("1 -1", "2 1"))
	tk.MustQuery("select id, v from t4 union select id, v from t3 order by id").
		Check(testkit.Rows("1 0", "2 1"))

	tk.MustExec("drop table if exists t5,t6,t7")
	tk.MustExec("create table t5 (id int, v bigint unsigned)")
	tk.MustExec("create table t6 (id int, v decimal)")
	tk.MustExec("create table t7 (id int, v bigint)")
	tk.MustExec("insert into t5 values (1, 1)")
	tk.MustExec("insert into t6 values (2, -1)")
	tk.MustExec("insert into t7 values (3, -1)")
	tk.MustQuery("select id, v from t5 union select id, v from t6 order by id").
		Check(testkit.Rows("1 1", "2 -1"))
	tk.MustQuery("select id, v from t5 union select id, v from t7 union select id, v from t6 order by id").
		Check(testkit.Rows("1 1", "2 -1", "3 -1"))
}

func (s *testSuite3) TestUpdateJoin(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t1, t2, t3, t4, t5, t6, t7")
	tk.MustExec("create table t1(k int, v int)")
	tk.MustExec("create table t2(k int, v int)")
	tk.MustExec("create table t3(id int auto_increment, k int, v int, primary key(id))")
	tk.MustExec("create table t4(k int, v int)")
	tk.MustExec("create table t5(v int, k int, primary key(k))")
	tk.MustExec("insert into t1 values (1, 1)")
	tk.MustExec("insert into t4 values (3, 3)")
	tk.MustExec("create table t6 (id int, v longtext)")
	tk.MustExec("create table t7 (x int, id int, v longtext, primary key(id))")

	// test the normal case that update one row for a single table.
	tk.MustExec("update t1 set v = 0 where k = 1")
	tk.MustQuery("select k, v from t1 where k = 1").Check(testkit.Rows("1 0"))

	// test the case that the table with auto_increment or none-null columns as the right table of left join.
	tk.MustExec("update t1 left join t3 on t1.k = t3.k set t1.v = 1")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 1"))
	tk.MustQuery("select id, k, v from t3").Check(testkit.Rows())

	// test left join and the case that the right table has no matching record but has updated the right table columns.
	tk.MustExec("update t1 left join t2 on t1.k = t2.k set t1.v = t2.v, t2.v = 3")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 <nil>"))
	tk.MustQuery("select k, v from t2").Check(testkit.Rows())

	// test the case that the update operation in the left table references data in the right table while data of the right table columns is modified.
	tk.MustExec("update t1 left join t2 on t1.k = t2.k set t2.v = 3, t1.v = t2.v")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 <nil>"))
	tk.MustQuery("select k, v from t2").Check(testkit.Rows())

	// test right join and the case that the left table has no matching record but has updated the left table columns.
	tk.MustExec("update t2 right join t1 on t2.k = t1.k set t2.v = 4, t1.v = 0")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 0"))
	tk.MustQuery("select k, v from t2").Check(testkit.Rows())

	// test the case of right join and left join at the same time.
	tk.MustExec("update t1 left join t2 on t1.k = t2.k right join t4 on t4.k = t2.k set t1.v = 4, t2.v = 4, t4.v = 4")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 0"))
	tk.MustQuery("select k, v from t2").Check(testkit.Rows())
	tk.MustQuery("select k, v from t4").Check(testkit.Rows("3 4"))

	// test normal left join and the case that the right table has matching rows.
	tk.MustExec("insert t2 values (1, 10)")
	tk.MustExec("update t1 left join t2 on t1.k = t2.k set t2.v = 11")
	tk.MustQuery("select k, v from t2").Check(testkit.Rows("1 11"))

	// test the case of continuously joining the same table and updating the unmatching records.
	tk.MustExec("update t1 t11 left join t2 on t11.k = t2.k left join t1 t12 on t2.v = t12.k set t12.v = 233, t11.v = 111")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("1 111"))
	tk.MustQuery("select k, v from t2").Check(testkit.Rows("1 11"))

	// test the left join case that the left table has records but all records are null.
	tk.MustExec("delete from t1")
	tk.MustExec("delete from t2")
	tk.MustExec("insert into t1 values (null, null)")
	tk.MustExec("update t1 left join t2 on t1.k = t2.k set t1.v = 1")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("<nil> 1"))

	// test the case that the right table of left join has an primary key.
	tk.MustExec("insert t5 values(0, 0)")
	tk.MustExec("update t1 left join t5 on t1.k = t5.k set t1.v = 2")
	tk.MustQuery("select k, v from t1").Check(testkit.Rows("<nil> 2"))
	tk.MustQuery("select k, v from t5").Check(testkit.Rows("0 0"))

	tk.MustExec("insert into t6 values (1, NULL)")
	tk.MustExec("insert into t7 values (5, 1, 'a')")
	tk.MustExec("update t6, t7 set t6.v = t7.v where t6.id = t7.id and t7.x = 5")
	tk.MustQuery("select v from t6").Check(testkit.Rows("a"))
}

func (s *testSuite3) TestMaxOneRow(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec(`use test`)
	tk.MustExec(`drop table if exists t1`)
	tk.MustExec(`drop table if exists t2`)
	tk.MustExec(`create table t1(a double, b double);`)
	tk.MustExec(`create table t2(a double, b double);`)
	tk.MustExec(`insert into t1 values(1, 1), (2, 2), (3, 3);`)
	tk.MustExec(`insert into t2 values(0, 0);`)
	tk.MustExec(`set @@tidb_init_chunk_size=1;`)
	rs, err := tk.Exec(`select (select t1.a from t1 where t1.a > t2.a) as a from t2;`)
	c.Assert(err, IsNil)

	err = rs.Next(context.TODO(), rs.NewChunk())
	c.Assert(err.Error(), Equals, "subquery returns more than 1 row")

	err = rs.Close()
	c.Assert(err, IsNil)
}

func (s *testSuite3) TestDoSubquery(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec(`use test`)
	tk.MustExec(`drop table if exists t`)
	tk.MustExec(`create table t(a int)`)
	_, err := tk.Exec(`do 1 in (select * from t)`)
	c.Assert(err, IsNil, Commentf("err %v", err))
	tk.MustExec(`insert into t values(1)`)
	r, err := tk.Exec(`do 1 in (select * from t)`)
	c.Assert(err, IsNil, Commentf("err %v", err))
	c.Assert(r, IsNil, Commentf("result of Do not empty"))
}

func (s *testSuite3) TestTSOFail(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec(`use test`)
	tk.MustExec(`drop table if exists t`)
	tk.MustExec(`create table t(a int)`)

	c.Assert(failpoint.Enable("github.com/pingcap/tidb/session/mockGetTSFail", "return"), IsNil)
	ctx := failpoint.WithHook(context.Background(), func(ctx context.Context, fpname string) bool {
		return fpname == "github.com/pingcap/tidb/session/mockGetTSFail"
	})
	_, err := tk.Se.Execute(ctx, `select * from t`)
	c.Assert(err, NotNil)
	c.Assert(failpoint.Disable("github.com/pingcap/tidb/session/mockGetTSFail"), IsNil)
}

type testSuite2 struct {
	*baseTestSuite
}

func (s *testSuite2) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite3 struct {
	*baseTestSuite
}

func (s *testSuite3) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite4 struct {
	*baseTestSuite
}

func (s *testSuite4) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite5 struct {
	*baseTestSuite
}

func (s *testSuite5) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite6 struct {
	*baseTestSuite
}

func (s *testSuite6) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite7 struct {
	*baseTestSuite
}

func (s *testSuite7) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

type testSuite8 struct {
	*baseTestSuite
}

func (s *testSuite8) TearDownTest(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show full tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

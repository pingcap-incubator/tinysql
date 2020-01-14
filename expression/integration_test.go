// Copyright 2017 PingCAP, Inc.
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

package expression_test

import (
	"bytes"
	"context"
	"fmt"
	. "github.com/pingcap/check"
	"github.com/pingcap/errors"
	"github.com/pingcap/parser/mysql"
	"github.com/pingcap/parser/terror"
	"github.com/pingcap/tidb/domain"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/kv"
	plannercore "github.com/pingcap/tidb/planner/core"
	"github.com/pingcap/tidb/session"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessionctx/variable"
	"github.com/pingcap/tidb/store/mockstore"
	"github.com/pingcap/tidb/util/mock"
	"github.com/pingcap/tidb/util/testkit"
	"github.com/pingcap/tidb/util/testutil"
	"sort"
	"strconv"
)

var _ = Suite(&testIntegrationSuite{})
var _ = Suite(&testIntegrationSuite2{})

type testIntegrationSuite struct {
	store kv.Storage
	dom   *domain.Domain
	ctx   sessionctx.Context
}

type testIntegrationSuite2 struct {
	testIntegrationSuite
}

func (s *testIntegrationSuite) cleanEnv(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	r := tk.MustQuery("show tables")
	for _, tb := range r.Rows() {
		tableName := tb[0]
		tk.MustExec(fmt.Sprintf("drop table %v", tableName))
	}
}

func (s *testIntegrationSuite) SetUpSuite(c *C) {
	var err error
	s.store, s.dom, err = newStoreWithBootstrap()
	c.Assert(err, IsNil)
	s.ctx = mock.NewContext()
}

func (s *testIntegrationSuite) TearDownSuite(c *C) {
	s.dom.Close()
	s.store.Close()
}

func (s *testIntegrationSuite) TestFuncREPEAT(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec("USE test;")
	tk.MustExec("DROP TABLE IF EXISTS table_string;")
	tk.MustExec("CREATE TABLE table_string(a CHAR(20), b VARCHAR(20), c TINYTEXT, d TEXT(20), e MEDIUMTEXT, f LONGTEXT, g BIGINT);")
	tk.MustExec("INSERT INTO table_string (a, b, c, d, e, f, g) VALUES ('a', 'b', 'c', 'd', 'e', 'f', 2);")
	tk.CheckExecResult(1, 0)

	r := tk.MustQuery("SELECT REPEAT(a, g), REPEAT(b, g), REPEAT(c, g), REPEAT(d, g), REPEAT(e, g), REPEAT(f, g) FROM table_string;")
	r.Check(testkit.Rows("aa bb cc dd ee ff"))

	r = tk.MustQuery("SELECT REPEAT(NULL, g), REPEAT(NULL, g), REPEAT(NULL, g), REPEAT(NULL, g), REPEAT(NULL, g), REPEAT(NULL, g) FROM table_string;")
	r.Check(testkit.Rows("<nil> <nil> <nil> <nil> <nil> <nil>"))

	r = tk.MustQuery("SELECT REPEAT(a, NULL), REPEAT(b, NULL), REPEAT(c, NULL), REPEAT(d, NULL), REPEAT(e, NULL), REPEAT(f, NULL) FROM table_string;")
	r.Check(testkit.Rows("<nil> <nil> <nil> <nil> <nil> <nil>"))

	r = tk.MustQuery("SELECT REPEAT(a, 2), REPEAT(b, 2), REPEAT(c, 2), REPEAT(d, 2), REPEAT(e, 2), REPEAT(f, 2) FROM table_string;")
	r.Check(testkit.Rows("aa bb cc dd ee ff"))

	r = tk.MustQuery("SELECT REPEAT(NULL, 2), REPEAT(NULL, 2), REPEAT(NULL, 2), REPEAT(NULL, 2), REPEAT(NULL, 2), REPEAT(NULL, 2) FROM table_string;")
	r.Check(testkit.Rows("<nil> <nil> <nil> <nil> <nil> <nil>"))

	r = tk.MustQuery("SELECT REPEAT(a, -1), REPEAT(b, -2), REPEAT(c, -2), REPEAT(d, -2), REPEAT(e, -2), REPEAT(f, -2) FROM table_string;")
	r.Check(testkit.Rows("     "))

	r = tk.MustQuery("SELECT REPEAT(a, 0), REPEAT(b, 0), REPEAT(c, 0), REPEAT(d, 0), REPEAT(e, 0), REPEAT(f, 0) FROM table_string;")
	r.Check(testkit.Rows("     "))

	r = tk.MustQuery("SELECT REPEAT(a, 16777217), REPEAT(b, 16777217), REPEAT(c, 16777217), REPEAT(d, 16777217), REPEAT(e, 16777217), REPEAT(f, 16777217) FROM table_string;")
	r.Check(testkit.Rows("<nil> <nil> <nil> <nil> <nil> <nil>"))
}

func (s *testIntegrationSuite) TestFuncLpadAndRpad(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec(`USE test;`)
	tk.MustExec(`DROP TABLE IF EXISTS t;`)
	tk.MustExec(`CREATE TABLE t(a BINARY(10), b CHAR(10));`)
	tk.MustExec(`INSERT INTO t SELECT "中文", "abc";`)
	result := tk.MustQuery(`SELECT LPAD(a, 11, "a"), LPAD(b, 2, "xx") FROM t;`)
	result.Check(testkit.Rows("a中文\x00\x00\x00\x00 ab"))
	result = tk.MustQuery(`SELECT RPAD(a, 11, "a"), RPAD(b, 2, "xx") FROM t;`)
	result.Check(testkit.Rows("中文\x00\x00\x00\x00a ab"))
	result = tk.MustQuery(`SELECT LPAD("中文", 5, "字符"), LPAD("中文", 1, "a");`)
	result.Check(testkit.Rows("字符字中文 中"))
	result = tk.MustQuery(`SELECT RPAD("中文", 5, "字符"), RPAD("中文", 1, "a");`)
	result.Check(testkit.Rows("中文字符字 中"))
	result = tk.MustQuery(`SELECT RPAD("中文", -5, "字符"), RPAD("中文", 10, "");`)
	result.Check(testkit.Rows("<nil> <nil>"))
	result = tk.MustQuery(`SELECT LPAD("中文", -5, "字符"), LPAD("中文", 10, "");`)
	result.Check(testkit.Rows("<nil> <nil>"))
}

func (s *testIntegrationSuite) TestOpBuiltin(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")

	// for logicAnd
	result := tk.MustQuery("select 1 && 1, 1 && 0, 0 && 1, 0 && 0, 2 && -1, null && 1, '1a' && 'a'")
	result.Check(testkit.Rows("1 0 0 0 1 <nil> 0"))
	// for bitNeg
	result = tk.MustQuery("select ~123, ~-123, ~null")
	result.Check(testkit.Rows("18446744073709551492 122 <nil>"))
	// for logicNot
	result = tk.MustQuery("select !1, !123, !0, !null")
	result.Check(testkit.Rows("0 0 1 <nil>"))
	// for logicalXor
	result = tk.MustQuery("select 1 xor 1, 1 xor 0, 0 xor 1, 0 xor 0, 2 xor -1, null xor 1, '1a' xor 'a'")
	result.Check(testkit.Rows("0 1 1 0 0 <nil> 1"))
	// for bitAnd
	result = tk.MustQuery("select 123 & 321, -123 & 321, null & 1")
	result.Check(testkit.Rows("65 257 <nil>"))
	// for bitOr
	result = tk.MustQuery("select 123 | 321, -123 | 321, null | 1")
	result.Check(testkit.Rows("379 18446744073709551557 <nil>"))
	// for bitXor
	result = tk.MustQuery("select 123 ^ 321, -123 ^ 321, null ^ 1")
	result.Check(testkit.Rows("314 18446744073709551300 <nil>"))
	// for leftShift
	result = tk.MustQuery("select 123 << 2, -123 << 2, null << 1")
	result.Check(testkit.Rows("492 18446744073709551124 <nil>"))
	// for rightShift
	result = tk.MustQuery("select 123 >> 2, -123 >> 2, null >> 1")
	result.Check(testkit.Rows("30 4611686018427387873 <nil>"))
	// for logicOr
	result = tk.MustQuery("select 1 || 1, 1 || 0, 0 || 1, 0 || 0, 2 || -1, null || 1, '1a' || 'a'")
	result.Check(testkit.Rows("1 1 1 0 1 1 1"))
	// for unaryPlus
	result = tk.MustQuery(`select +1, +0, +(-9), +(-0.001), +0.999, +null, +"aaa"`)
	result.Check(testkit.Rows("1 0 -9 -0.001 0.999 <nil> aaa"))
	// for unaryMinus
	tk.MustExec("drop table if exists f")
	tk.MustExec("create table f(a decimal(65,0))")
	tk.MustExec("insert into f value (-17000000000000000000)")
	result = tk.MustQuery("select a from f")
	result.Check(testkit.Rows("-17000000000000000000"))
}

func (s *testIntegrationSuite) TestAggregationBuiltin(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("create table t(a decimal(7, 6))")
	tk.MustExec("insert into t values(1.123456), (1.123456)")
	result := tk.MustQuery("select avg(a) from t")
	result.Check(testkit.Rows("1.1234560000"))

	tk.MustExec("use test")
	tk.MustExec("drop table t")
	tk.MustExec("CREATE TABLE `t` (	`a` int, KEY `idx_a` (`a`))")
	result = tk.MustQuery("select avg(a) from t")
	result.Check(testkit.Rows("<nil>"))
	result = tk.MustQuery("select max(a), min(a) from t")
	result.Check(testkit.Rows("<nil> <nil>"))
	result = tk.MustQuery("select distinct a from t")
	result.Check(testkit.Rows())
	result = tk.MustQuery("select sum(a) from t")
	result.Check(testkit.Rows("<nil>"))
	result = tk.MustQuery("select count(a) from t")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("18446744073709551615"))
}

func (s *testIntegrationSuite) TestAggregationBuiltinBitOr(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t;")
	tk.MustExec("create table t(a bigint)")
	tk.MustExec("insert into t values(null);")
	result := tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("0"))
	tk.MustExec("insert into t values(1);")
	result = tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("1"))
	tk.MustExec("insert into t values(2);")
	result = tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("3"))
	tk.MustExec("insert into t values(4);")
	result = tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("7"))
	result = tk.MustQuery("select a, bit_or(a) from t group by a order by a")
	result.Check(testkit.Rows("<nil> 0", "1 1", "2 2", "4 4"))
	tk.MustExec("insert into t values(-1);")
	result = tk.MustQuery("select bit_or(a) from t")
	result.Check(testkit.Rows("18446744073709551615"))
}

func (s *testIntegrationSuite) TestAggregationBuiltinBitXor(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t;")
	tk.MustExec("create table t(a bigint)")
	tk.MustExec("insert into t values(null);")
	result := tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("0"))
	tk.MustExec("insert into t values(1);")
	result = tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("1"))
	tk.MustExec("insert into t values(2);")
	result = tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("3"))
	tk.MustExec("insert into t values(3);")
	result = tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("0"))
	tk.MustExec("insert into t values(3);")
	result = tk.MustQuery("select bit_xor(a) from t")
	result.Check(testkit.Rows("3"))
	result = tk.MustQuery("select a, bit_xor(a) from t group by a order by a")
	result.Check(testkit.Rows("<nil> 0", "1 1", "2 2", "3 0"))
}

func (s *testIntegrationSuite) TestAggregationBuiltinBitAnd(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t;")
	tk.MustExec("create table t(a bigint)")
	tk.MustExec("insert into t values(null);")
	result := tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("18446744073709551615"))
	tk.MustExec("insert into t values(7);")
	result = tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("7"))
	tk.MustExec("insert into t values(5);")
	result = tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("5"))
	tk.MustExec("insert into t values(3);")
	result = tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("1"))
	tk.MustExec("insert into t values(2);")
	result = tk.MustQuery("select bit_and(a) from t")
	result.Check(testkit.Rows("0"))
	result = tk.MustQuery("select a, bit_and(a) from t group by a order by a desc")
	result.Check(testkit.Rows("7 7", "5 5", "3 3", "2 2", "<nil> 18446744073709551615"))
}

func (s *testIntegrationSuite) TestAggregationBuiltinGroupConcat(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec("create table t(a varchar(100))")
	tk.MustExec("create table d(a varchar(100))")
	tk.MustExec("insert into t values('hello'), ('hello')")
	result := tk.MustQuery("select group_concat(a) from t")
	result.Check(testkit.Rows("hello,hello"))

	tk.MustExec("set @@group_concat_max_len=7")
	result = tk.MustQuery("select group_concat(a) from t")
	result.Check(testkit.Rows("hello,h"))
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Warning 1260 Some rows were cut by GROUPCONCAT(test.t.a)"))

	_, err := tk.Exec("insert into d select group_concat(a) from t")
	c.Assert(errors.Cause(err).(*terror.Error).Code(), Equals, terror.ErrCode(mysql.ErrCutValueGroupConcat))

	tk.Exec("set sql_mode=''")
	tk.MustExec("insert into d select group_concat(a) from t")
	tk.MustQuery("show warnings").Check(testutil.RowsWithSep("|", "Warning 1260 Some rows were cut by GROUPCONCAT(test.t.a)"))
	tk.MustQuery("select * from d").Check(testkit.Rows("hello,h"))
}

func (s *testIntegrationSuite) TestLiterals(c *C) {
	defer s.cleanEnv(c)
	tk := testkit.NewTestKit(c, s.store)
	r := tk.MustQuery("SELECT LENGTH(b''), LENGTH(B''), b''+1, b''-1, B''+1;")
	r.Check(testkit.Rows("0 0 1 -1 1"))
}

func (s *testIntegrationSuite) TestInPredicate4UnsignedInt(c *C) {
	// for issue #6661
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("CREATE TABLE t (a bigint unsigned,key (a));")
	tk.MustExec("INSERT INTO t VALUES (0), (4), (5), (6), (7), (8), (9223372036854775810), (18446744073709551614), (18446744073709551615);")
	r := tk.MustQuery(`SELECT a FROM t WHERE a NOT IN (-1, -2, 18446744073709551615);`)
	r.Check(testkit.Rows("0", "4", "5", "6", "7", "8", "9223372036854775810", "18446744073709551614"))
	r = tk.MustQuery(`SELECT a FROM t WHERE a NOT IN (-1, -2, 4, 9223372036854775810);`)
	r.Check(testkit.Rows("0", "5", "6", "7", "8", "18446744073709551614", "18446744073709551615"))
	r = tk.MustQuery(`SELECT a FROM t WHERE a NOT IN (-1, -2, 0, 4, 18446744073709551614);`)
	r.Check(testkit.Rows("5", "6", "7", "8", "9223372036854775810", "18446744073709551615"))

	// for issue #4473
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t1 (some_id smallint(5) unsigned,key (some_id) )")
	tk.MustExec("insert into t1 values (1),(2)")
	r = tk.MustQuery(`select some_id from t1 where some_id not in(2,-1);`)
	r.Check(testkit.Rows("1"))
}

func (s *testIntegrationSuite) TestFilterExtractFromDNF(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec("use test")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t(a int, b int, c int)")

	tests := []struct {
		exprStr string
		result  string
	}{
		{
			exprStr: "a = 1 or a = 1 or a = 1",
			result:  "[eq(test.t.a, 1)]",
		},
		{
			exprStr: "a = 1 or a = 1 or (a = 1 and b = 1)",
			result:  "[eq(test.t.a, 1)]",
		},
		{
			exprStr: "(a = 1 and a = 1) or a = 1 or b = 1",
			result:  "[or(or(and(eq(test.t.a, 1), eq(test.t.a, 1)), eq(test.t.a, 1)), eq(test.t.b, 1))]",
		},
		{
			exprStr: "(a = 1 and b = 2) or (a = 1 and b = 3) or (a = 1 and b = 4)",
			result:  "[eq(test.t.a, 1) or(eq(test.t.b, 2), or(eq(test.t.b, 3), eq(test.t.b, 4)))]",
		},
		{
			exprStr: "(a = 1 and b = 1 and c = 1) or (a = 1 and b = 1) or (a = 1 and b = 1 and c > 2 and c < 3)",
			result:  "[eq(test.t.a, 1) eq(test.t.b, 1)]",
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		sql := "select * from t where " + tt.exprStr
		sctx := tk.Se.(sessionctx.Context)
		sc := sctx.GetSessionVars().StmtCtx
		stmts, err := session.Parse(sctx, sql)
		c.Assert(err, IsNil, Commentf("error %v, for expr %s", err, tt.exprStr))
		c.Assert(stmts, HasLen, 1)
		is := domain.GetDomain(sctx).InfoSchema()
		err = plannercore.Preprocess(sctx, stmts[0], is)
		c.Assert(err, IsNil, Commentf("error %v, for resolve name, expr %s", err, tt.exprStr))
		p, _, err := plannercore.BuildLogicalPlan(ctx, sctx, stmts[0], is)
		c.Assert(err, IsNil, Commentf("error %v, for build plan, expr %s", err, tt.exprStr))
		selection := p.(plannercore.LogicalPlan).Children()[0].(*plannercore.LogicalSelection)
		conds := make([]expression.Expression, len(selection.Conditions))
		for i, cond := range selection.Conditions {
			conds[i] = expression.PushDownNot(sctx, cond)
		}
		afterFunc := expression.ExtractFiltersFromDNFs(sctx, conds)
		sort.Slice(afterFunc, func(i, j int) bool {
			return bytes.Compare(afterFunc[i].HashCode(sc), afterFunc[j].HashCode(sc)) < 0
		})
		c.Assert(fmt.Sprintf("%s", afterFunc), Equals, tt.result, Commentf("wrong result for expr: %s", tt.exprStr))
	}
}

func newStoreWithBootstrap() (kv.Storage, *domain.Domain, error) {
	store, err := mockstore.NewMockTikvStore()
	if err != nil {
		return nil, nil, err
	}
	session.SetSchemaLease(0)
	dom, err := session.BootstrapSession(store)
	return store, dom, err
}

func (s *testIntegrationSuite) TestTwoDecimalTruncate(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec("use test")
	tk.MustExec("set sql_mode=''")
	tk.MustExec("drop table if exists t")
	tk.MustExec("create table t1(a decimal(10,5), b decimal(10,1))")
	tk.MustExec("insert into t1 values(123.12345, 123.12345)")
	tk.MustExec("update t1 set b = a")
	res := tk.MustQuery("select a, b from t1")
	res.Check(testkit.Rows("123.12345 123.1"))
	res = tk.MustQuery("select 2.00000000000000000000000000000001 * 1.000000000000000000000000000000000000000000002")
	res.Check(testkit.Rows("2.000000000000000000000000000000"))
}

func (s *testIntegrationSuite) TestPrefixIndex(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	defer s.cleanEnv(c)
	tk.MustExec("use test")
	tk.MustExec(`CREATE TABLE t1 (
  			name varchar(12) DEFAULT NULL,
  			KEY pname (name(12))
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`)

	tk.MustExec("insert into t1 values('借款策略集_网页');")
	res := tk.MustQuery("select * from t1 where name = '借款策略集_网页';")
	res.Check(testkit.Rows("借款策略集_网页"))

	tk.MustExec(`CREATE TABLE prefix (
		a int(11) NOT NULL,
		b varchar(55) DEFAULT NULL,
		c int(11) DEFAULT NULL,
		PRIMARY KEY (a),
		KEY prefix_index (b(2)),
		KEY prefix_complex (a,b(2))
	) ENGINE=InnoDB DEFAULT CHARSET=utf8 COLLATE=utf8_bin;`)

	tk.MustExec("INSERT INTO prefix VALUES(0, 'b', 2), (1, 'bbb', 3), (2, 'bbc', 4), (3, 'bbb', 5), (4, 'abc', 6), (5, 'abc', 7), (6, 'abc', 7), (7, 'ÿÿ', 8), (8, 'ÿÿ0', 9), (9, 'ÿÿÿ', 10);")
	res = tk.MustQuery("select c, b from prefix where b > 'ÿ' and b < 'ÿÿc'")
	res.Check(testkit.Rows("8 ÿÿ", "9 ÿÿ0"))
}

func (s *testIntegrationSuite) TestDecimalMul(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("USE test")
	tk.MustExec("create table t(a decimal(38, 17));")
	tk.MustExec("insert into t select 0.5999991229316*0.918755041726043;")
	res := tk.MustQuery("select * from t;")
	res.Check(testkit.Rows("0.55125221922461136"))
}

func (s *testIntegrationSuite) TestUnknowHintIgnore(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("USE test")
	tk.MustExec("create table t(a int)")
	tk.MustQuery("select /*+ unknown_hint(c1)*/ 1").Check(testkit.Rows("1"))
	tk.MustQuery("show warnings").Check(testkit.Rows("Warning 1064 You have an error in your SQL syntax; check the manual that corresponds to your TiDB version for the right syntax to use line 1 column 29 near \"unknown_hint(c1)*/ 1\" "))
	_, err := tk.Exec("select 1 from /*+ test1() */ t")
	c.Assert(err, NotNil)
}

func (s *testIntegrationSuite) TestForeignKeyVar(c *C) {

	tk := testkit.NewTestKit(c, s.store)

	tk.MustExec("SET FOREIGN_KEY_CHECKS=1")
	tk.MustQuery("SHOW WARNINGS").Check(testkit.Rows("Warning 8047 variable 'foreign_key_checks' does not yet support value: 1"))
}

func (s *testIntegrationSuite) TestValuesFloat32(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t (i int key, j float);`)
	tk.MustExec(`insert into t values (1, 0.01);`)
	tk.MustQuery(`select * from t;`).Check(testkit.Rows(`1 0.01`))
	tk.MustExec(`insert into t values (1, 0.02) on duplicate key update j = values (j);`)
	tk.MustQuery(`select * from t;`).Check(testkit.Rows(`1 0.02`))
}

func (s *testIntegrationSuite) TestValuesEnum(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t (a bigint primary key, b enum('a','b','c'));`)
	tk.MustExec(`insert into t values (1, "a");`)
	tk.MustQuery(`select * from t;`).Check(testkit.Rows(`1 a`))
	tk.MustExec(`insert into t values (1, "b") on duplicate key update b = values(b);`)
	tk.MustQuery(`select * from t;`).Check(testkit.Rows(`1 b`))
}

func (s *testIntegrationSuite) TestIssue10181(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t(a bigint unsigned primary key);`)
	tk.MustExec(`insert into t values(9223372036854775807), (18446744073709551615)`)
	tk.MustQuery(`select * from t where a > 9223372036854775807-0.5 order by a`).Check(testkit.Rows(`9223372036854775807`, `18446744073709551615`))
}

func (s *testIntegrationSuite) TestExprPushdownBlacklist(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustQuery(`select * from mysql.expr_pushdown_blacklist`).Check(testkit.Rows())
}

func (s *testIntegrationSuite) TestOptRuleBlacklist(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustQuery(`select * from mysql.opt_rule_blacklist`).Check(testkit.Rows())
}

func (s *testIntegrationSuite) TestIssue10804(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustQuery(`SELECT @@information_schema_stats_expiry`).Check(testkit.Rows(`86400`))
	tk.MustExec("/*!80000 SET SESSION information_schema_stats_expiry=0 */")
	tk.MustQuery(`SELECT @@information_schema_stats_expiry`).Check(testkit.Rows(`0`))
	tk.MustQuery(`SELECT @@GLOBAL.information_schema_stats_expiry`).Check(testkit.Rows(`86400`))
	tk.MustExec("/*!80000 SET GLOBAL information_schema_stats_expiry=0 */")
	tk.MustQuery(`SELECT @@GLOBAL.information_schema_stats_expiry`).Check(testkit.Rows(`0`))
}

func (s *testIntegrationSuite) TestInvalidEndingStatement(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	parseErrMsg := "[parser:1064]"
	errMsgLen := len(parseErrMsg)

	assertParseErr := func(sql string) {
		_, err := tk.Exec(sql)
		c.Assert(err, NotNil)
		c.Assert(err.Error()[:errMsgLen], Equals, parseErrMsg)
	}

	assertParseErr("drop table if exists t'xyz")
	assertParseErr("drop table if exists t'")
	assertParseErr("drop table if exists t`")
	assertParseErr(`drop table if exists t'`)
	assertParseErr(`drop table if exists t"`)
}

func (s *testIntegrationSuite) TestIssue10675(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use test")
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t(a int);`)
	tk.MustExec(`insert into t values(1);`)
	tk.MustQuery(`select * from t where a < -184467440737095516167.1;`).Check(testkit.Rows())
	tk.MustQuery(`select * from t where a > -184467440737095516167.1;`).Check(
		testkit.Rows("1"))
	tk.MustQuery(`select * from t where a < 184467440737095516167.1;`).Check(
		testkit.Rows("1"))
	tk.MustQuery(`select * from t where a > 184467440737095516167.1;`).Check(testkit.Rows())

	// issue 11647
	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t(b bit(1));`)
	tk.MustExec(`insert into t values(b'1');`)
	tk.MustQuery(`select count(*) from t where b = 1;`).Check(testkit.Rows("1"))
	tk.MustQuery(`select count(*) from t where b = '1';`).Check(testkit.Rows("1"))
	tk.MustQuery(`select count(*) from t where b = b'1';`).Check(testkit.Rows("1"))

	tk.MustExec(`drop table if exists t;`)
	tk.MustExec(`create table t(b bit(63));`)
	// Not 64, because the behavior of mysql is amazing. I have no idea to fix it.
	tk.MustExec(`insert into t values(b'111111111111111111111111111111111111111111111111111111111111111');`)
	tk.MustQuery(`select count(*) from t where b = 9223372036854775807;`).Check(testkit.Rows("1"))
	tk.MustQuery(`select count(*) from t where b = '9223372036854775807';`).Check(testkit.Rows("1"))
	tk.MustQuery(`select count(*) from t where b = b'111111111111111111111111111111111111111111111111111111111111111';`).Check(testkit.Rows("1"))
}

func (s *testIntegrationSuite) TestDefEnableVectorizedEvaluation(c *C) {
	tk := testkit.NewTestKit(c, s.store)
	tk.MustExec("use mysql")
	tk.MustQuery(`select @@tidb_enable_vectorized_expression`).Check(testkit.Rows("1"))
}

func (s *testIntegrationSuite) TestDecodetoChunkReuse(c *C) {
	tk := testkit.NewTestKitWithInit(c, s.store)
	tk.MustExec("create table chk (a int,b varchar(20))")
	for i := 0; i < 200; i++ {
		if i%5 == 0 {
			tk.MustExec(fmt.Sprintf("insert chk values (NULL,NULL)"))
			continue
		}
		tk.MustExec(fmt.Sprintf("insert chk values (%d,'%s')", i, strconv.Itoa(i)))
	}

	tk.Se.GetSessionVars().DistSQLScanConcurrency = 1
	tk.MustExec("set tidb_init_chunk_size = 2")
	tk.MustExec("set tidb_max_chunk_size = 32")
	defer func() {
		tk.MustExec(fmt.Sprintf("set tidb_init_chunk_size = %d", variable.DefInitChunkSize))
		tk.MustExec(fmt.Sprintf("set tidb_max_chunk_size = %d", variable.DefMaxChunkSize))
	}()
	rs, err := tk.Exec("select * from chk")
	c.Assert(err, IsNil)
	req := rs.NewChunk()
	var count int
	for {
		err = rs.Next(context.TODO(), req)
		c.Assert(err, IsNil)
		numRows := req.NumRows()
		if numRows == 0 {
			break
		}
		for i := 0; i < numRows; i++ {
			if count%5 == 0 {
				c.Assert(req.GetRow(i).IsNull(0), Equals, true)
				c.Assert(req.GetRow(i).IsNull(1), Equals, true)
			} else {
				c.Assert(req.GetRow(i).IsNull(0), Equals, false)
				c.Assert(req.GetRow(i).IsNull(1), Equals, false)
				c.Assert(req.GetRow(i).GetInt64(0), Equals, int64(count))
				c.Assert(req.GetRow(i).GetString(1), Equals, strconv.Itoa(count))
			}
			count++
		}
	}
	c.Assert(count, Equals, 200)
	rs.Close()
}

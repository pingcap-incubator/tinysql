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

package server

import (
	"database/sql"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	. "github.com/pingcap/check"
	"github.com/pingcap/log"
	tmysql "github.com/pingcap/parser/mysql"
	"github.com/pingcap/tidb/util/logutil"
	"go.uber.org/zap"
)

func TestT(t *testing.T) {
	CustomVerboseFlag = true
	logLevel := os.Getenv("log_level")
	logutil.InitZapLogger(logutil.NewLogConfig(logLevel, logutil.DefaultLogFormat, logutil.EmptyFileLogConfig, false))
	TestingT(t)
}

var regression = true

var defaultDSNConfig = mysql.Config{
	User:   "root",
	Net:    "tcp",
	Addr:   "127.0.0.1:4001",
	DBName: "test",
	Strict: true,
}

type configOverrider func(*mysql.Config)

// getDSN generates a DSN string for MySQL connection.
func getDSN(overriders ...configOverrider) string {
	var config = defaultDSNConfig
	for _, overrider := range overriders {
		if overrider != nil {
			overrider(&config)
		}
	}
	return config.FormatDSN()
}

// runTests runs tests using the default database `test`.
func runTests(c *C, overrider configOverrider, tests ...func(dbt *DBTest)) {
	db, err := sql.Open("mysql", getDSN(overrider))
	c.Assert(err, IsNil, Commentf("Error connecting"))
	defer db.Close()

	db.Exec("DROP TABLE IF EXISTS test")

	dbt := &DBTest{c, db}
	for _, test := range tests {
		test(dbt)
		dbt.db.Exec("DROP TABLE IF EXISTS test")
	}
}

// runTestsOnNewDB runs tests using a specified database which will be created before the test and destroyed after the test.
func runTestsOnNewDB(c *C, overrider configOverrider, dbName string, tests ...func(dbt *DBTest)) {
	dsn := getDSN(overrider, func(config *mysql.Config) {
		config.DBName = ""
	})
	db, err := sql.Open("mysql", dsn)
	c.Assert(err, IsNil, Commentf("Error connecting"))
	defer db.Close()

	_, err = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;", dbName))
	c.Assert(err, IsNil, Commentf("Error drop database %s: %s", dbName, err))

	_, err = db.Exec(fmt.Sprintf("CREATE DATABASE `%s`;", dbName))
	c.Assert(err, IsNil, Commentf("Error create database %s: %s", dbName, err))

	defer func() {
		_, err = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;", dbName))
		c.Assert(err, IsNil, Commentf("Error drop database %s: %s", dbName, err))
	}()

	_, err = db.Exec(fmt.Sprintf("USE `%s`;", dbName))
	c.Assert(err, IsNil, Commentf("Error use database %s: %s", dbName, err))

	dbt := &DBTest{c, db}
	for _, test := range tests {
		test(dbt)
		dbt.db.Exec("DROP TABLE IF EXISTS test")
	}
}

type DBTest struct {
	*C
	db *sql.DB
}

func (dbt *DBTest) mustPrepare(query string) *sql.Stmt {
	stmt, err := dbt.db.Prepare(query)
	dbt.Assert(err, IsNil, Commentf("Prepare %s", query))
	return stmt
}

func (dbt *DBTest) mustExecPrepared(stmt *sql.Stmt, args ...interface{}) sql.Result {
	res, err := stmt.Exec(args...)
	dbt.Assert(err, IsNil, Commentf("Execute prepared with args: %s", args))
	return res
}

func (dbt *DBTest) mustQueryPrepared(stmt *sql.Stmt, args ...interface{}) *sql.Rows {
	rows, err := stmt.Query(args...)
	dbt.Assert(err, IsNil, Commentf("Query prepared with args: %s", args))
	return rows
}

func (dbt *DBTest) mustExec(query string, args ...interface{}) (res sql.Result) {
	res, err := dbt.db.Exec(query, args...)
	dbt.Assert(err, IsNil, Commentf("Exec %s", query))
	return res
}

func (dbt *DBTest) mustQuery(query string, args ...interface{}) (rows *sql.Rows) {
	rows, err := dbt.db.Query(query, args...)
	dbt.Assert(err, IsNil, Commentf("Query %s", query))
	return rows
}

func (dbt *DBTest) mustQueryRows(query string, args ...interface{}) {
	rows := dbt.mustQuery(query, args...)
	dbt.Assert(rows.Next(), IsTrue)
	rows.Close()
}

func runTestRegression(c *C, overrider configOverrider, dbName string) {
	runTestsOnNewDB(c, overrider, dbName, func(dbt *DBTest) {
		// Create Table
		dbt.mustExec("CREATE TABLE test (val TINYINT)")

		// Test for unexpected data
		var out bool
		rows := dbt.mustQuery("SELECT * FROM test")
		dbt.Assert(rows.Next(), IsFalse, Commentf("unexpected data in empty table"))

		// Create Data
		res := dbt.mustExec("INSERT INTO test VALUES (1)")
		//		res := dbt.mustExec("INSERT INTO test VALUES (?)", 1)
		count, err := res.RowsAffected()
		dbt.Assert(err, IsNil)
		dbt.Check(count, Equals, int64(1))
		id, err := res.LastInsertId()
		dbt.Assert(err, IsNil)
		dbt.Check(id, Equals, int64(0))

		// Read
		rows = dbt.mustQuery("SELECT val FROM test")
		if rows.Next() {
			rows.Scan(&out)
			dbt.Check(out, IsTrue)
			dbt.Check(rows.Next(), IsFalse, Commentf("unexpected data"))
		} else {
			dbt.Error("no data")
		}
		rows.Close()

		// Update
		res = dbt.mustExec("UPDATE test SET val = 0 WHERE val = ?", 1)
		count, err = res.RowsAffected()
		dbt.Assert(err, IsNil)
		dbt.Check(count, Equals, int64(1))

		// Check Update
		rows = dbt.mustQuery("SELECT val FROM test")
		if rows.Next() {
			rows.Scan(&out)
			dbt.Check(out, IsFalse)
			dbt.Check(rows.Next(), IsFalse, Commentf("unexpected data"))
		} else {
			dbt.Error("no data")
		}
		rows.Close()

		// Delete
		res = dbt.mustExec("DELETE FROM test WHERE val = 0")
		//		res = dbt.mustExec("DELETE FROM test WHERE val = ?", 0)
		count, err = res.RowsAffected()
		dbt.Assert(err, IsNil)
		dbt.Check(count, Equals, int64(1))

		// Check for unexpected rows
		res = dbt.mustExec("DELETE FROM test")
		count, err = res.RowsAffected()
		dbt.Assert(err, IsNil)
		dbt.Check(count, Equals, int64(0))

		dbt.mustQueryRows("SELECT 1")

		var b = make([]byte, 0)
		if err := dbt.db.QueryRow("SELECT ?", b).Scan(&b); err != nil {
			dbt.Fatal(err)
		}
		if b == nil {
			dbt.Error("nil echo from non-nil input")
		}
	})
}

func runTestPrepareResultFieldType(t *C) {
	var param int64 = 83
	runTests(t, nil, func(dbt *DBTest) {
		stmt, err := dbt.db.Prepare(`SELECT ?`)
		if err != nil {
			dbt.Fatal(err)
		}
		defer stmt.Close()
		row := stmt.QueryRow(param)
		var result int64
		err = row.Scan(&result)
		if err != nil {
			dbt.Fatal(err)
		}
		switch {
		case result != param:
			dbt.Fatal("Unexpected result value")
		}
	})
}

func runTestSpecialType(t *C) {
	runTestsOnNewDB(t, nil, "SpecialType", func(dbt *DBTest) {
		dbt.mustExec("create table test (a decimal(10, 5), b datetime, c time, d bit(8))")
		dbt.mustExec("insert test values (1.4, '2012-12-21 12:12:12', '4:23:34', b'1000')")
		rows := dbt.mustQuery("select * from test where a > ?", 0)
		t.Assert(rows.Next(), IsTrue)
		var outA float64
		var outB, outC string
		var outD []byte
		err := rows.Scan(&outA, &outB, &outC, &outD)
		t.Assert(err, IsNil)
		t.Assert(outA, Equals, 1.4)
		t.Assert(outB, Equals, "2012-12-21 12:12:12")
		t.Assert(outC, Equals, "04:23:34")
		t.Assert(outD, BytesEquals, []byte{8})
	})
}

func runTestClientWithCollation(t *C) {
	runTests(t, func(config *mysql.Config) {
		config.Collation = "utf8mb4_general_ci"
	}, func(dbt *DBTest) {
		var name, charset, collation string
		// check session variable collation_connection
		rows := dbt.mustQuery("show variables like 'collation_connection'")
		t.Assert(rows.Next(), IsTrue)
		err := rows.Scan(&name, &collation)
		t.Assert(err, IsNil)
		t.Assert(collation, Equals, "utf8mb4_general_ci")

		// check session variable character_set_client
		rows = dbt.mustQuery("show variables like 'character_set_client'")
		t.Assert(rows.Next(), IsTrue)
		err = rows.Scan(&name, &charset)
		t.Assert(err, IsNil)
		t.Assert(charset, Equals, "utf8mb4")

		// check session variable character_set_results
		rows = dbt.mustQuery("show variables like 'character_set_results'")
		t.Assert(rows.Next(), IsTrue)
		err = rows.Scan(&name, &charset)
		t.Assert(err, IsNil)
		t.Assert(charset, Equals, "utf8mb4")

		// check session variable character_set_connection
		rows = dbt.mustQuery("show variables like 'character_set_connection'")
		t.Assert(rows.Next(), IsTrue)
		err = rows.Scan(&name, &charset)
		t.Assert(err, IsNil)
		t.Assert(charset, Equals, "utf8mb4")
	})
}

func runTestPreparedString(t *C) {
	runTestsOnNewDB(t, nil, "PreparedString", func(dbt *DBTest) {
		dbt.mustExec("create table test (a char(10), b char(10))")
		dbt.mustExec("insert test values (?, ?)", "abcdeabcde", "abcde")
		rows := dbt.mustQuery("select * from test where 1 = ?", 1)
		t.Assert(rows.Next(), IsTrue)
		var outA, outB string
		err := rows.Scan(&outA, &outB)
		t.Assert(err, IsNil)
		t.Assert(outA, Equals, "abcdeabcde")
		t.Assert(outB, Equals, "abcde")
	})
}

// runTestPreparedTimestamp does not really cover binary timestamp format, because MySQL driver in golang
// does not use this format. MySQL driver in golang will convert the timestamp to a string.
// This case guarantees it could work.
func runTestPreparedTimestamp(t *C) {
	runTestsOnNewDB(t, nil, "prepared_timestamp", func(dbt *DBTest) {
		dbt.mustExec("create table test (a timestamp, b time)")
		dbt.mustExec("set time_zone='+00:00'")
		insertStmt := dbt.mustPrepare("insert test values (?, ?)")
		defer insertStmt.Close()
		vts := time.Unix(1, 1)
		vt := time.Unix(-1, 1)
		dbt.mustExecPrepared(insertStmt, vts, vt)
		selectStmt := dbt.mustPrepare("select * from test where a = ? and b = ?")
		defer selectStmt.Close()
		rows := dbt.mustQueryPrepared(selectStmt, vts, vt)
		t.Assert(rows.Next(), IsTrue)
		var outA, outB string
		err := rows.Scan(&outA, &outB)
		t.Assert(err, IsNil)
		t.Assert(outA, Equals, "1970-01-01 00:00:01")
		t.Assert(outB, Equals, "23:59:59")
	})
}

func runTestConcurrentUpdate(c *C) {
	dbName := "Concurrent"
	runTestsOnNewDB(c, nil, dbName, func(dbt *DBTest) {
		dbt.mustExec("drop table if exists test2")
		dbt.mustExec("create table test2 (a int, b int)")
		dbt.mustExec("insert test2 values (1, 1)")
		dbt.mustExec("set @@tidb_disable_txn_auto_retry = 0")

		txn1, err := dbt.db.Begin()
		c.Assert(err, IsNil)
		_, err = txn1.Exec(fmt.Sprintf("USE `%s`;", dbName))
		c.Assert(err, IsNil)

		txn2, err := dbt.db.Begin()
		c.Assert(err, IsNil)
		_, err = txn2.Exec(fmt.Sprintf("USE `%s`;", dbName))
		c.Assert(err, IsNil)

		_, err = txn2.Exec("update test2 set a = a + 1 where b = 1")
		c.Assert(err, IsNil)
		err = txn2.Commit()
		c.Assert(err, IsNil)

		_, err = txn1.Exec("update test2 set a = a + 1 where b = 1")
		c.Assert(err, IsNil)

		err = txn1.Commit()
		c.Assert(err, IsNil)
	})
}

func runTestErrorCode(c *C) {
	runTestsOnNewDB(c, nil, "ErrorCode", func(dbt *DBTest) {
		dbt.mustExec("create table test (c int PRIMARY KEY);")
		dbt.mustExec("insert into test values (1);")
		txn1, err := dbt.db.Begin()
		c.Assert(err, IsNil)
		_, err = txn1.Exec("insert into test values(1)")
		c.Assert(err, IsNil)
		err = txn1.Commit()
		checkErrorCode(c, err, tmysql.ErrDupEntry)

		// Schema errors
		txn2, err := dbt.db.Begin()
		c.Assert(err, IsNil)
		_, err = txn2.Exec("use db_not_exists;")
		checkErrorCode(c, err, tmysql.ErrBadDB)
		_, err = txn2.Exec("select * from tbl_not_exists;")
		checkErrorCode(c, err, tmysql.ErrNoSuchTable)
		_, err = txn2.Exec("create database test;")
		// Make tests stable. Some times the error may be the ErrInfoSchemaChanged.
		checkErrorCode(c, err, tmysql.ErrDBCreateExists, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("create database aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;")
		checkErrorCode(c, err, tmysql.ErrTooLongIdent, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("create table test (c int);")
		checkErrorCode(c, err, tmysql.ErrTableExists, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("drop table unknown_table;")
		checkErrorCode(c, err, tmysql.ErrBadTable, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("drop database unknown_db;")
		checkErrorCode(c, err, tmysql.ErrDBDropExists, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("create table aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa (a int);")
		checkErrorCode(c, err, tmysql.ErrTooLongIdent, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("create table long_column_table (aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa int);")
		checkErrorCode(c, err, tmysql.ErrTooLongIdent, tmysql.ErrInfoSchemaChanged)
		_, err = txn2.Exec("alter table test add aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa int;")
		checkErrorCode(c, err, tmysql.ErrTooLongIdent, tmysql.ErrInfoSchemaChanged)

		// Optimizer errors
		_, err = txn2.Exec("select *, * from test;")
		checkErrorCode(c, err, tmysql.ErrInvalidWildCard)
		_, err = txn2.Exec("select row(1, 2) > 1;")
		checkErrorCode(c, err, tmysql.ErrOperandColumns)
		_, err = txn2.Exec("select * from test order by row(c, c);")
		checkErrorCode(c, err, tmysql.ErrOperandColumns)

		// Variable errors
		_, err = txn2.Exec("select @@unknown_sys_var;")
		checkErrorCode(c, err, tmysql.ErrUnknownSystemVariable)
		_, err = txn2.Exec("set @@unknown_sys_var='1';")
		checkErrorCode(c, err, tmysql.ErrUnknownSystemVariable)

		// Expression errors
		_, err = txn2.Exec("select greatest(2);")
		checkErrorCode(c, err, tmysql.ErrWrongParamcountToNativeFct)
	})
}

func checkErrorCode(c *C, e error, codes ...uint16) {
	me, ok := e.(*mysql.MySQLError)
	c.Assert(ok, IsTrue, Commentf("err: %v", e))
	if len(codes) == 1 {
		c.Assert(me.Number, Equals, codes[0])
	}
	isMatchCode := false
	for _, code := range codes {
		if me.Number == code {
			isMatchCode = true
			break
		}
	}
	c.Assert(isMatchCode, IsTrue, Commentf("got err %v, expected err codes %v", me, codes))
}

func runTestIssue3662(c *C) {
	db, err := sql.Open("mysql", getDSN(func(config *mysql.Config) {
		config.DBName = "non_existing_schema"
	}))
	c.Assert(err, IsNil)
	defer db.Close()

	// According to documentation, "Open may just validate its arguments without
	// creating a connection to the database. To verify that the data source name
	// is valid, call Ping."
	err = db.Ping()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "Error 1049: Unknown database 'non_existing_schema'")
}

func runTestDBNameEscape(c *C) {
	runTests(c, nil, func(dbt *DBTest) {
		dbt.mustExec("CREATE DATABASE `aa-a`;")
	})
	runTests(c, func(config *mysql.Config) {
		config.DBName = "aa-a"
	}, func(dbt *DBTest) {
		dbt.mustExec(`USE mysql;`)
		dbt.mustExec("DROP DATABASE `aa-a`")
	})
}

func runTestResultFieldTableIsNull(c *C) {
	runTestsOnNewDB(c, nil, "ResultFieldTableIsNull", func(dbt *DBTest) {
		dbt.mustExec("drop table if exists test;")
		dbt.mustExec("create table test (c int);")
		dbt.mustExec("explain select * from test;")
	})
}

func runTestMultiStatements(c *C) {
	runTestsOnNewDB(c, nil, "MultiStatements", func(dbt *DBTest) {
		// Create Table
		dbt.mustExec("CREATE TABLE `test` (`id` int(11) NOT NULL, `value` int(11) NOT NULL) ")

		// Create Data
		res := dbt.mustExec("INSERT INTO test VALUES (1, 1)")
		count, err := res.RowsAffected()
		c.Assert(err, IsNil, Commentf("res.RowsAffected() returned error"))
		c.Assert(count, Equals, int64(1))

		// Update
		res = dbt.mustExec("UPDATE test SET value = 3 WHERE id = 1; UPDATE test SET value = 4 WHERE id = 1; UPDATE test SET value = 5 WHERE id = 1;")
		count, err = res.RowsAffected()
		c.Assert(err, IsNil, Commentf("res.RowsAffected() returned error"))
		c.Assert(count, Equals, int64(1))

		// Read
		var out int
		rows := dbt.mustQuery("SELECT value FROM test WHERE id=1;")
		if rows.Next() {
			rows.Scan(&out)
			c.Assert(out, Equals, 5)

			if rows.Next() {
				dbt.Error("unexpected data")
			}
		} else {
			dbt.Error("no data")
		}
	})
}

func runTestSumAvg(c *C) {
	runTests(c, nil, func(dbt *DBTest) {
		dbt.mustExec("create table sumavg (a int, b decimal, c double)")
		dbt.mustExec("insert sumavg values (1, 1, 1)")
		rows := dbt.mustQuery("select sum(a), sum(b), sum(c) from sumavg")
		c.Assert(rows.Next(), IsTrue)
		var outA, outB, outC float64
		err := rows.Scan(&outA, &outB, &outC)
		c.Assert(err, IsNil)
		c.Assert(outA, Equals, 1.0)
		c.Assert(outB, Equals, 1.0)
		c.Assert(outC, Equals, 1.0)
		rows = dbt.mustQuery("select avg(a), avg(b), avg(c) from sumavg")
		c.Assert(rows.Next(), IsTrue)
		err = rows.Scan(&outA, &outB, &outC)
		c.Assert(err, IsNil)
		c.Assert(outA, Equals, 1.0)
		c.Assert(outB, Equals, 1.0)
		c.Assert(outC, Equals, 1.0)
	})
}

const retryTime = 100

func waitUntilServerOnline(statusPort uint) {
	// connect server
	retry := 0
	for ; retry < retryTime; retry++ {
		time.Sleep(time.Millisecond * 10)
		db, err := sql.Open("mysql", getDSN())
		if err == nil {
			db.Close()
			break
		}
	}
	if retry == retryTime {
		log.Fatal("failed to connect DB in every 10 ms", zap.Int("retryTime", retryTime))
	}
	// connect http status
	statusURL := fmt.Sprintf("http://127.0.0.1:%d/status", statusPort)
	for retry = 0; retry < retryTime; retry++ {
		resp, err := http.Get(statusURL)
		if err == nil {
			ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	if retry == retryTime {
		log.Fatal("failed to connect HTTP status in every 10 ms", zap.Int("retryTime", retryTime))
	}
}

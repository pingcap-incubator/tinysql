# TinySQL

TinySQL is a course designed to teach you how to implement a distributed relational database in Go. TinySQL is also the name of the simplifed version of [TiDB](https://github.com/pingcap/tidb).

## Prerequisites

Experience with Go is required. If not, it is recommended to learn [A Tour of Go](https://tour.golang.org/) first.

## Course Overview

The detailed information you can get in the directory `courses`.

There's a [material list](./courses/material.md). It lists plenty of materials for you to study how a database system works. We picks some of the topics in it and prepare homework for you to have a better understand about it.

This course will take you from idea to implementation, with the essential topics of distributed relational database covered. 

The course is organized into three parts:

1. Gives a simple interpretation of SQL and relational algebra in preparation for the following course.

2. Explains the life of a read-only SQL, which includes parsing, execution, and the optimization of SQL plans.

3. Focuses on SQLs (including DML and DDL) that change the state of the database: how they get implemented and how to deal with the interaction of them and read-only statements.

## Other courses in this series

This course only focuses on the SQL layer of a distributed database system. If you are also interested in KV layer, see [TinyKV](https://github.com/pingcap-incubator/tinykv).

## Deploy

Once you finish the project. You can deploy the binary and use MySQL client to connect the server.

### Build

```
make
```

### Run & Play

Use `./bin/tidb-server` to deploy the server and use `mysql -h127.0.0.1 -P4000 -uroot` to connect the server.

Also you can deploy the cluster together with the tinykv project.

Use `make` command in tinykv project to build binary `tinyscheduler-server`, `tinykv-server`.

Then put the binaries into a single dir and run the following commands:

```
mkdir -p data
./tinyscheduler-server
./tinykv-server -path=data
./tidb-server --store=tikv --path="127.0.0.1:2379"
```

## Contributing

Contributions are welcomed and greatly appreciated. See [Contribution Guide](https://github.com/pingcap/community/tree/master/contributors) for details on submitting patches and the contribution workflow.

## License

TinySQL is under the Apache 2.0 license. See the [LICENSE](./LICENSE) file for details.

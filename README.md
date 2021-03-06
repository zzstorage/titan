# Titan

[![Build Status](https://travis-ci.org/distributedio/titan.svg?branch=master)](https://travis-ci.org/distributedio/titan)
[![Go Report Card](https://goreportcard.com/badge/github.com/distributedio/titan)](https://goreportcard.com/report/github.com/distributedio/titan)
[![Coverage Status](https://coveralls.io/repos/github/distributedio/titan/badge.svg?branch=master)](https://coveralls.io/github/distributedio/titan?branch=master)
[![Coverage Status](https://img.shields.io/badge/version-v0.3.1-brightgreen.svg)](https://github.com/distributedio/titan/releases)
[![Discourse status](https://img.shields.io/discourse/https/meta.discourse.org/status.svg)](https://titan-tech-group.slack.com)

A distributed implementation of __Redis compatible layer__  based on [TiKV](https://github.com/tikv/tikv/)

## Why Titan?
* Completely compatible with redis protocol
* Full distributed transaction with strong consistency
* Multi-tenancy support
* No painful scale out
* High availability 

Thanks [TiKV](https://github.com/tikv/tikv/) for supporting the core features. The project is developed and open sourced by the Beijing Infrastructure Team at [Meitu](https://www.meitu.com/) and has been donated to [DistributedIO](https://github.com/distributedio) org.

## Architecture

___Arch about titan___

![titan](docs/titan.png)

___Arch about TiKV___

![TiKV softwares](https://github.com/tikv/tikv/blob/master/images/tikv_stack.png)

## Quick start

Can't wait to experiment Titan? Just follow 2 steps:

1. curl -s -O https://raw.githubusercontent.com/distributedio/titan/master/docker-compose.yml
2. docker-compose up

Then connect to titan use redis-cli

> redis-cli -p 7369

___Enjoy!___

## Installation

### SetUp TiKV cluster
Titan works with 2 tidb components:
* TiKV
* Pd

To setup TiKV and PD, please follow official [instruction](https://pingcap.com/docs-cn/dev/how-to/deploy/orchestrated/ansible/)

### Run Titan

* Build the binary

```
go get github.com/distributedio/titan
cd $GOPATH/src/github.com/distributedio/titan
make 
```

* Edit the configration file

```
pd-addrs="tikv://your-pd-addrs:port"
```

* Run titan

```
./titan
```

For more details about [Deploy Titan](docs/ops/deploy.md), click here.

## Benchmarks

Basic benchmarking result.

### Get

![Get command benchmark](docs/benchmark/get-benchmark.png)

### Set

![Set command benchmark](docs/benchmark/set-benchmark.png)

For more info, please vist here [Titan Benchmarks](docs/benchmark/benchmark.md)

## Commands supporting status

For fully supported command list vist [here](docs/command_list.md)

|command|status|
|---|---|
|Connections|Won't Fully Supported|
|Transactions|Supported|
|Server|Not Fully Supported Yet|
|Keys|Not Fully Supported Yet|
|Strings|Not Fully Supported Yet|
|List|Not Fully Supported Yet|
|Hashes|Supported|
|Sets|Not Fully Supported Yet|
|Sorted Sets|Not Fully Supported Yet|
|Geo| Not Supported Yet|
|Hyperloglog| Not Supported Yet|
|Pub/Sub| Not Supported Yet|
|Scripting| Not Supported Yet|
|Streams| Not Supported Yet|

## FAQ

[FAQ](https://github.com/distributedio/titan/issues?utf8=%E2%9C%93&q=+label%3A%22good+first+issue%22)

## Roadmap

View our [Roadmap](https://github.com/distributedio/titan/projects)

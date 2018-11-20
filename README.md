# gcp-ips

## Motivation

Currently (November 2018), there is no easy way to list all IPs in-use (or reserved but not in-use) in a particular subnet in a shared VPC. IP addresses are attached to resources (e.g. an instance or a forwarding rule) which are listed in service projects. This means that one has to scan through all service projects exhaustively, collect all IPs in-use or reserved, and then organize them by subnets

## Setup

Follow the recommended authentication method described [here](https://cloud.google.com/docs/authentication/getting-started)

Basically:
```
export GOOGLE_APPLICATION_CREDENTIALS=<path to service account key>
```

## Run

```
go run main.go <host-project>
```
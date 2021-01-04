[![Report card](https://goreportcard.com/badge/github.com/rafaelrubbioli/terminator)](https://goreportcard.com/report/github.com/rafaelrubbioli/terminator)
<a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License MIT"></a>

# K8S OOM Terminator
When a pod reaches `OOM` (out of memory) state, K8S sends a `SIGKILL` and immediately kills pod. This may cause some errors when the pod is processing something (like an HTTP request).

Terminator is a way to mitigate that scenario. It watches pods and sends a SIGTERM when they reach the defined limit and lets them terminate gracefully.


## Note
This is not a solution, just a way to help minimize errors when pods reach OOM. Thus, this should only be used while you find the problem.

## Usage
It is possible to run terminator:

- Locally: simply clone this repository and run it!

- On a deployment inside K8S: https://github.com/rafaelrubbioli/terminator/blob/master/deployment.yaml

- On Docker: docker.pkg.github.com/rafaelrubbioli/terminator/terminator:latest

## Flags
`config`(string): kube config file path, default is incluster config

`local`(bool): use local config .kube/config file

`dry-run`(bool): will not send SIGTERM to pods, only log when they reach the limit

`debug`(bool): if set will log all steps

`namespace`(string): namespace to look for pods, if empty gets all namespaces

`services`([]string): services to get the pods

`deployments`([]string): deployments to get pods

`limit`(int): memory usage percentage limit

`sleep`(int): duration in milliseconds to sleep between checks

`kill-sleep`(int): duration in milliseconds to sleep after killing a pod

`kill-after`(int): amount of checks the pod needs to be over limit to be killed

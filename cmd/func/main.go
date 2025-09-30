package main

import "knative.dev/func/pkg/app"

// main <- app <- cmd
// TODO 这个代码架构设计不好
func main() {
	app.Main()
}

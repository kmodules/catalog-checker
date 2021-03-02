package main

import (
	"fmt"
	shell "github.com/codeskyblue/go-sh"
	"kmodules.xyz/client-go/tools/parser"
	"os"
)

func main() {
	sh := shell.NewSession()
	sh.SetDir(os.ExpandEnv("$HOME/go/src/kubedb.dev/installer"))
	sh.ShowCMD = true


	out, err := sh.Command("helm", "template", "charts/kubedb-catalog").Output()
	if err != nil {
		panic(err)
	}
	resources, err := parser.ListResources(out)
	if err != nil {
		panic(err)
	}

	fmt.Println(len(resources))
}

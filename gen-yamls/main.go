package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver"
	shell "github.com/codeskyblue/go-sh"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"kmodules.xyz/catalog-checker/semvers"
	"kmodules.xyz/client-go/tools/parser"
	"kmodules.xyz/resource-metadata/hub"
	"sigs.k8s.io/yaml"
)

var reg = hub.NewRegistryOfKnownResources()

func main() {
	sh := shell.NewSession()
	sh.SetDir(os.ExpandEnv("$HOME/go/src/kubedb.dev/installer"))
	sh.ShowCMD = true

	out, err := sh.Command("helm", "template", "charts/kubedb-catalog", "--set", "skipDeprecated=false").Output()
	if err != nil {
		panic(err)
	}
	resources, err := parser.ListResources(out)
	if err != nil {
		panic(err)
	}

	fmt.Println(len(resources))

	dir := filepath.Join(".", "raw")
	err = os.RemoveAll(dir)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		panic(err)
	}

	type DbVersion struct {
		Group   string
		Kind    string
		Version string
	}

	store := map[DbVersion][]*unstructured.Unstructured{}
	for _, obj := range resources {
		gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
		if err != nil {
			panic(err)
		}

		version, _, err := unstructured.NestedString(obj.Object, "spec", "version")
		if err != nil {
			panic(err)
		}
		key := DbVersion{
			Group:   gv.Group,
			Kind:    obj.GetKind(),
			Version: version,
		}
		store[key] = append(store[key], obj)
	}

	for k, v := range store {
		if k.Group == "catalog.kubedb.com" {

			sort.Slice(v, func(i, j int) bool {
				di, _, _ := unstructured.NestedBool(v[i].Object, "spec", "deprecated")
				dj, _, _ := unstructured.NestedBool(v[j].Object, "spec", "deprecated")

				if (di && dj) || (!di && !dj) {
					// company version
					vi, err := semver.NewVersion(v[i].GetName())
					if err != nil {
						panic(fmt.Errorf("%s reason: %v", v[i].GetName(), err))
					}
					vj, err := semver.NewVersion(v[j].GetName())
					if err != nil {
						panic(fmt.Errorf("%s reason: %v", v[j].GetName(), err))
					}
					return semvers.CompareVersions(vi, vj)
				}
				return dj // or !di
			})
			store[k] = v

			dbKind := strings.TrimSuffix(k.Kind, "Version")
			filename := filepath.Join(dir, strings.ToLower(dbKind), fmt.Sprintf("%s-%s.yaml", strings.ToLower(dbKind), k.Version))
			err = os.MkdirAll(filepath.Dir(filename), 0755)
			if err != nil {
				panic(err)
			}

			var buf bytes.Buffer
			for i, obj := range v {
				if i > 0 {
					buf.WriteString("\n---\n")
				}
				data, err := yaml.Marshal(obj)
				if err != nil {
					panic(err)
				}
				buf.Write(data)
			}
			err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
			if err != nil {
				panic(err)
			}
		}
	}
}

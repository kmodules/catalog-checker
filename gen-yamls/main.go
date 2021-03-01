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

	type DB struct {
		Group string
		Kind  string
	}
	type DbVersion struct {
		Group   string
		Kind    string
		Version string
	}

	dbStore := map[DbVersion][]*unstructured.Unstructured{}
	pspForDBs := map[DB][]string{}
	pspStore := map[string]*unstructured.Unstructured{}

	for _, obj := range resources {
		gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
		if err != nil {
			panic(err)
		}
		if gv.Group == "catalog.kubedb.com" {
			version, _, err := unstructured.NestedString(obj.Object, "spec", "version")
			if err != nil {
				panic(err)
			}
			key := DbVersion{
				Group:   gv.Group,
				Kind:    obj.GetKind(),
				Version: version,
			}
			dbStore[key] = append(dbStore[key], obj)

			pspName, _, err := unstructured.NestedString(obj.Object, "spec", "podSecurityPolicies", "databasePolicyName")
			if err != nil {
				panic(err)
			}
			if pspName != "" {
				key2 := DB{
					Group: gv.Group,
					Kind:  obj.GetKind(),
				}
				pspForDBs[key2] = append(pspForDBs[key2], pspName)
			}
		} else if gv.Group == "policy" {
			if _, ok := pspStore[obj.GetName()]; ok {
				panic("duplicate PSP name " + obj.GetName())
			}
			pspStore[obj.GetName()] = obj
		}
	}

	for k, v := range dbStore {
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
		dbStore[k] = v

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

		dbKind := strings.TrimSuffix(k.Kind, "Version")
		filename := filepath.Join(dir, strings.ToLower(dbKind), fmt.Sprintf("%s-%s.yaml", strings.ToLower(dbKind), k.Version))
		if allDeprecated(v) {
			filename = filepath.Join(dir, strings.ToLower(dbKind), fmt.Sprintf("deprecated-%s-%s.yaml", strings.ToLower(dbKind), k.Version))
		}
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
		if err != nil {
			panic(err)
		}
	}

	for k, v := range pspForDBs {
		if len(v) == 0 {
			continue
		}

		var buf bytes.Buffer
		for i, pspName := range v {
			if i > 0 {
				buf.WriteString("\n---\n")
			}
			data, err := yaml.Marshal(pspStore[pspName])
			if err != nil {
				panic(err)
			}
			buf.Write(data)
		}

		dbKind := strings.TrimSuffix(k.Kind, "Version")
		filename := filepath.Join(dir, strings.ToLower(dbKind), fmt.Sprintf("%s-psp.yaml", strings.ToLower(dbKind)))
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
		if err != nil {
			panic(err)
		}
	}
}

func allDeprecated(objs []*unstructured.Unstructured) bool {
	for _, obj := range objs{
		d, _, _ := unstructured.NestedBool(obj.Object, "spec", "deprecated")
		if !d {
			return false
		}
	}
	return true
}

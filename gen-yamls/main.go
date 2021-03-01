package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/util/sets"
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
	pspForDBs := map[DB]sets.String{}
	pspStore := map[string]*unstructured.Unstructured{}

	// active versions
	activeDBVersions := map[string][]string{}
	// backupTask -> db version
	backupTaskStore := map[string][]string{}
	// recoveryTask -> db version
	restoreTaskStore := map[string][]string{}

	for _, obj := range resources {
		gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
		if err != nil {
			panic(err)
		}
		if gv.Group == "catalog.kubedb.com" {
			dbKind := strings.TrimSuffix(obj.GetKind(), "Version")
			deprecated, _, _ := unstructured.NestedBool(obj.Object, "spec", "deprecated")

			version, _, err := unstructured.NestedString(obj.Object, "spec", "version")
			if err != nil {
				panic(err)
			}
			dbverKey := DbVersion{
				Group:   gv.Group,
				Kind:    obj.GetKind(),
				Version: version,
			}
			dbStore[dbverKey] = append(dbStore[dbverKey], obj)

			pspName, _, err := unstructured.NestedString(obj.Object, "spec", "podSecurityPolicies", "databasePolicyName")
			if err != nil {
				panic(err)
			}
			if pspName != "" {
				dbKey := DB{
					Group: gv.Group,
					Kind:  obj.GetKind(),
				}
				if _, ok := pspForDBs[dbKey]; !ok {
					pspForDBs[dbKey] = sets.NewString()
				}
				pspForDBs[dbKey].Insert(pspName)
			}

			if !deprecated {
				activeDBVersions[dbKind] = append(activeDBVersions[dbKind], obj.GetName())

				backupTask, _, _ := unstructured.NestedString(obj.Object, "spec", "stash", "addon", "backupTask", "name")
				if backupTask != "" {
					backupTaskStore[backupTask] = append(backupTaskStore[backupTask], obj.GetName())
				}
				restoreTask, _, _ := unstructured.NestedString(obj.Object, "spec", "stash", "addon", "restoreTask", "name")
				if restoreTask != "" {
					restoreTaskStore[backupTask] = append(restoreTaskStore[backupTask], obj.GetName())
				}
			}
		} else if gv.Group == "policy" {
			if _, ok := pspStore[obj.GetName()]; ok {
				panic("duplicate PSP name " + obj.GetName())
			}
			pspStore[obj.GetName()] = obj
		}
	}

	{
		for k, v := range activeDBVersions {
			versions, err := semvers.SortVersions(v)
			if err != nil {
				panic(err)
			}
			activeDBVersions[k] = versions
		}

		data, err := json.MarshalIndent(activeDBVersions, "", "  ")
		if err != nil {
			panic(err)
		}

		filename := filepath.Join(dir, "active_dbs.json")
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, data, 0644)
		if err != nil {
			panic(err)
		}
	}

	{
		ds := map[string]string{}

		for k, v := range backupTaskStore {
			versions, err := semvers.SortVersions(v)
			if err != nil {
				panic(err)
			}
			backupTaskStore[k] = versions
			ds[k] = versions[0]
		}

		data, err := json.MarshalIndent(ds, "", "  ")
		if err != nil {
			panic(err)
		}

		filename := filepath.Join(dir, "backup_tasks.json")
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, data, 0644)
		if err != nil {
			panic(err)
		}
	}

	{
		ds := map[string]string{}

		for k, v := range restoreTaskStore {
			versions, err := semvers.SortVersions(v)
			if err != nil {
				panic(err)
			}
			restoreTaskStore[k] = versions
			ds[k] = versions[0]
		}

		data, err := json.MarshalIndent(ds, "", "  ")
		if err != nil {
			panic(err)
		}

		filename := filepath.Join(dir, "restore_tasks.json")
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, data, 0644)
		if err != nil {
			panic(err)
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
		for i, pspName := range v.List() {
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
	for _, obj := range objs {
		d, _, _ := unstructured.NestedBool(obj.Object, "spec", "deprecated")
		if !d {
			return false
		}
	}
	return true
}

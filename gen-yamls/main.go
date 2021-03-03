package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Masterminds/semver"
	"github.com/Masterminds/sprig"
	shell "github.com/codeskyblue/go-sh"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
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

	dir := filepath.Join(".", "catalog")
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
		Distro string
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

			distro, _, _ := unstructured.NestedString(obj.Object, "spec", "distribution")
			if dbKind == "Elasticsearch" {
				authPlugin, _, _ := unstructured.NestedString(obj.Object, "spec", "authPlugin")
				if distro == "" {
					distro = authPlugin
					if authPlugin == "X-Pack" {
						distro = "ElasticStack"
					}
					err = unstructured.SetNestedField(obj.Object, distro, "spec", "distribution")
					if err != nil {
						panic(err)
					}
				}
			} else if dbKind == "MySQL" {
				distro = "Oracle"
				err = unstructured.SetNestedField(obj.Object, distro, "spec", "distribution")
				if err != nil {
					panic(err)
				}
			} else if dbKind == "MongoDB" {
				distro = "MongoDB"
				if strings.Contains(strings.ToLower(obj.GetName()), "percona") {
					distro = "Percona"
				}
				err = unstructured.SetNestedField(obj.Object, distro, "spec", "distribution")
				if err != nil {
					panic(err)
				}
			}

			version, _, err := unstructured.NestedString(obj.Object, "spec", "version")
			if err != nil {
				panic(err)
			}
			dbverKey := DbVersion{
				Group:   gv.Group,
				Kind:    obj.GetKind(),
				Version: version,
				Distro: distro,
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
					restoreTaskStore[restoreTask] = append(restoreTaskStore[restoreTask], obj.GetName())
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
		//ds := map[string]string{}

		for k, v := range backupTaskStore {
			versions, err := semvers.SortVersions(v)
			if err != nil {
				panic(err)
			}
			backupTaskStore[k] = versions
			//ds[k] = versions[0]
		}

		data, err := json.MarshalIndent(backupTaskStore, "", "  ")
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
		//ds := map[string]string{}

		for k, v := range restoreTaskStore {
			versions, err := semvers.SortVersions(v)
			if err != nil {
				panic(err)
			}
			restoreTaskStore[k] = versions
			//ds[k] = versions[0]
		}

		data, err := json.MarshalIndent(restoreTaskStore, "", "  ")
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

		var filenameparts []string
		if allDeprecated(v) {
			filenameparts = append(filenameparts, "deprecated")
		}
		filenameparts =append(filenameparts, strings.ToLower(dbKind), k.Version)
		if k.Distro != "" {
			filenameparts =append(filenameparts, strings.ToLower(k.Distro))
		}
		filename := filepath.Join(dir, "raw", strings.ToLower(dbKind), fmt.Sprintf("%s.yaml", strings.Join(filenameparts, "-")))
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
		filename := filepath.Join(dir, "raw", strings.ToLower(dbKind), fmt.Sprintf("%s-psp.yaml", strings.ToLower(dbKind)))
		err = os.MkdirAll(filepath.Dir(filename), 0755)
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
		if err != nil {
			panic(err)
		}
	}

	// GENERATE CHART
	{
		for k, v := range dbStore {
			for _, obj := range v {
				spec, _, err := unstructured.NestedMap(obj.Object, "spec")
				if err != nil {
					panic(err)
				}
				for prop := range spec {

					templatizeRegistry := func(field string) {
						img, ok, _ := unstructured.NestedString(obj.Object, "spec", prop, field)
						if ok {
							newimg := `{{ .Values.image.registry }}/` + strings.Split(img, "/")[1]
							unstructured.SetNestedField(obj.Object, newimg, "spec", prop, field)
						}
					}

					templatizeRegistry("image")
					templatizeRegistry("yqImage")
				}
			}

			{
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
				filename := filepath.Join(dir, "templates", strings.ToLower(dbKind), fmt.Sprintf("%s-%s.yaml", strings.ToLower(dbKind), k.Version))
				if allDeprecated(v) {
					filename = filepath.Join(dir, "templates", strings.ToLower(dbKind), fmt.Sprintf("deprecated-%s-%s.yaml", strings.ToLower(dbKind), k.Version))
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

			{
				dbKind := strings.TrimSuffix(k.Kind, "Version")

				var buf bytes.Buffer
				for i, obj := range v {
					if i > 0 {
						buf.WriteString("\n---\n")
					}

					data := map[string]interface{}{
						"key":    strings.ToLower(dbKind),
						"object": obj.Object,
					}
					localTplFile := "/home/tamal/go/src/kmodules.xyz/catalog-checker/gen-chart/template-dbver.yaml"
					funcMap := sprig.TxtFuncMap()
					funcMap["toYaml"] = toYAML
					funcMap["toJson"] = toJSON
					tpl := template.Must(template.New(filepath.Base(localTplFile)).Funcs(funcMap).ParseFiles(localTplFile))
					err = tpl.Execute(&buf, &data)
					if err != nil {
						panic(err)
					}
				}

				filename := filepath.Join(dir, "charts", "kubedb-catalog", "templates", strings.ToLower(dbKind), fmt.Sprintf("%s-%s.yaml", strings.ToLower(dbKind), k.Version))
				if allDeprecated(v) {
					filename = filepath.Join(dir, "charts", "kubedb-catalog", "templates", strings.ToLower(dbKind), fmt.Sprintf("deprecated-%s-%s.yaml", strings.ToLower(dbKind), k.Version))
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
		}

		for k, v := range pspForDBs {
			if len(v) == 0 {
				continue
			}

			dbKind := strings.TrimSuffix(k.Kind, "Version")

			var buf bytes.Buffer
			for i, pspName := range v.List() {
				if i > 0 {
					buf.WriteString("\n---\n")
				}

				if pspStore[pspName] == nil {
					panic("missing psp " + pspName + " for db " + dbKind)
				}

				data := map[string]interface{}{
					"key":    strings.ToLower(dbKind),
					"object": pspStore[pspName].Object,
				}
				localTplFile := "/home/tamal/go/src/kmodules.xyz/catalog-checker/gen-chart/template-psp.yaml"
				funcMap := sprig.TxtFuncMap()
				funcMap["toYaml"] = toYAML
				funcMap["toJson"] = toJSON
				tpl := template.Must(template.New(filepath.Base(localTplFile)).Funcs(funcMap).ParseFiles(localTplFile))
				err = tpl.Execute(&buf, &data)
				if err != nil {
					panic(err)
				}
			}

			filename := filepath.Join(dir, "charts", "kubedb-catalog", "templates", strings.ToLower(dbKind), fmt.Sprintf("%s-psp.yaml", strings.ToLower(dbKind)))
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

// toYAML takes an interface, marshals it to yaml, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toYAML(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

// toJSON takes an interface, marshals it to json, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return string(data)
}

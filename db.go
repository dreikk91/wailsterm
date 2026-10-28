package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
)

// DB is the document store behind electerm's dbAction(dbName, op, ...args).
// The client only uses equality queries ({} or {_id}), so each table is a JSON
// array of documents in its own file, encrypted at rest by protect/unprotect
// (DPAPI on Windows), the way electerm encrypts its tables with safeStorage.
// ponytail: whole-table rewrite per write; switch to sqlite if tables reach many MB.
type DB struct {
	dir    string
	mu     sync.Mutex
	tables map[string][]map[string]any
}

// electerm's table list (src/app/lib/nedb.js); anything else is rejected.
var dbTables = map[string]bool{
	"bookmarks": true, "bookmarkGroups": true, "addressBookmarks": true, "terminalThemes": true,
	"lastStates": true, "data": true, "quickCommands": true, "log": true, "dbUpgradeLog": true,
	"profiles": true, "workspaces": true, "triggers": true, "history": true,
	"terminalCommandHistory": true, "aiChatHistory": true, "autoRunWidgets": true,
}

func NewDB(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &DB{dir: dir, tables: map[string][]map[string]any{}}, nil
}

func (d *DB) table(name string) ([]map[string]any, error) {
	if !dbTables[name] {
		return nil, errors.New("unknown table " + name)
	}
	if t, ok := d.tables[name]; ok {
		return t, nil
	}
	var t []map[string]any
	raw, err := os.ReadFile(filepath.Join(d.dir, name+".db"))
	if err == nil {
		if raw, err = unprotect(raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	d.tables[name] = t
	return t, nil
}

func (d *DB) flush(name string) error {
	raw, err := json.Marshal(d.tables[name])
	if err != nil {
		return err
	}
	if raw, err = protect(raw); err != nil {
		return err
	}
	tmp := filepath.Join(d.dir, name+".db.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(d.dir, name+".db"))
}

func matches(doc, query map[string]any) bool {
	for k, v := range query {
		if !reflect.DeepEqual(doc[k], v) {
			return false
		}
	}
	return true
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Action implements the nedb subset electerm's client uses: find, findOne, insert, update, remove.
func (d *DB) Action(name, op string, args []any) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, err := d.table(name)
	if err != nil {
		return nil, err
	}
	arg := func(i int) map[string]any {
		if i < len(args) {
			return asMap(args[i])
		}
		return nil
	}
	switch op {
	case "find":
		out := []map[string]any{}
		for _, doc := range t {
			if matches(doc, arg(0)) {
				out = append(out, doc)
			}
		}
		return out, nil
	case "findOne":
		for _, doc := range t {
			if matches(doc, arg(0)) {
				return doc, nil
			}
		}
		return nil, nil
	case "insert":
		docs, single := []any{}, false
		if len(args) > 0 {
			if a, ok := args[0].([]any); ok {
				docs = a
			} else {
				docs, single = []any{args[0]}, true
			}
		}
		for _, v := range docs {
			doc := asMap(v)
			if doc == nil {
				continue
			}
			if doc["_id"] == nil {
				doc["_id"] = newID()
			}
			// unique _id: insert over an existing doc replaces it, like electerm's sqlite INSERT OR REPLACE
			t = upsert(t, doc)
		}
		d.tables[name] = t
		if err := d.flush(name); err != nil {
			return nil, err
		}
		if single && len(docs) == 1 {
			return docs[0], nil
		}
		return docs, nil
	case "update":
		query, upd, opts := arg(0), arg(1), arg(2)
		set, isSet := upd["$set"].(map[string]any)
		n := 0
		for i, doc := range t {
			if !matches(doc, query) {
				continue
			}
			if isSet {
				for k, v := range set {
					doc[k] = v
				}
			} else {
				nd := map[string]any{"_id": doc["_id"]}
				for k, v := range upd {
					if k != "_id" {
						nd[k] = v
					}
				}
				t[i] = nd
			}
			n++
			if opts["multi"] != true {
				break
			}
		}
		if n == 0 && opts["upsert"] == true {
			doc := map[string]any{}
			for k, v := range query {
				doc[k] = v
			}
			src := upd
			if isSet {
				src = set
			}
			for k, v := range src {
				doc[k] = v
			}
			if doc["_id"] == nil {
				doc["_id"] = newID()
			}
			t = append(t, doc)
			n = 1
		}
		d.tables[name] = t
		return n, d.flush(name)
	case "remove":
		query, opts := arg(0), arg(1)
		kept, n := t[:0:0], 0
		for _, doc := range t {
			if matches(doc, query) && (n == 0 || opts["multi"] == true) {
				n++
				continue
			}
			kept = append(kept, doc)
		}
		d.tables[name] = kept
		return n, d.flush(name)
	case "compactDatafile":
		return nil, nil
	}
	return nil, errors.New("unsupported db op " + op)
}

func upsert(t []map[string]any, doc map[string]any) []map[string]any {
	for i, old := range t {
		if reflect.DeepEqual(old["_id"], doc["_id"]) {
			t[i] = doc
			return t
		}
	}
	return append(t, doc)
}

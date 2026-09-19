package whatsapp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Milestone 1 rule: download code only reads messages.db. These are the only MessageStore
// methods it may call; both are checked to be read-only under SQLite's query_only mode by
// TestDownloadQueriesAreReadOnly in internal/database.
var allowedMessageStoreCalls = map[string]bool{
	"GetMediaInfo":      true,
	"ListAudioMessages": true,
}

// downloadCode lists the functions that make up the download path, per file
var downloadCode = map[string][]string{
	"download.go":             nil, // every function in the file
	"lid.go":                  nil,
	"media_decrypt.go":        nil,
	"../api/handlers.go":      {"handleDownloadMedia", "handleDownloadBatch", "runBatchDownload", "writeBatchIndexJSON", "handleBatchJobByID", "handleServeMedia"},
	"../api/server.go":        nil,
	"../database/messages.go": {"GetMediaInfo", "ListAudioMessages"},
}

// TestDownloadCodeOnlyReadsMessages fails if download code calls any messageStore method
// outside the allow-list, or runs SQL on the messages database directly.
func TestDownloadCodeOnlyReadsMessages(t *testing.T) {
	fset := token.NewFileSet()
	for file, funcs := range downloadCode {
		f, err := parser.ParseFile(fset, filepath.FromSlash(file), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		want := map[string]bool{}
		for _, fn := range funcs {
			want[fn] = true
		}
		found := map[string]bool{}

		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || (len(want) > 0 && !want[fd.Name.Name]) {
				continue
			}
			found[fd.Name.Name] = true
			inMessagesGo := file == "../database/messages.go"

			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				method := sel.Sel.Name
				recv := receiverName(sel.X)

				if recv == "messageStore" && !allowedMessageStoreCalls[method] {
					t.Errorf("%s: %s calls messageStore.%s, which is not in the read-only allow-list",
						file, fd.Name.Name, method)
				}
				// Inside the two read methods: only Query/QueryRow on the DB handle
				if inMessagesGo && recv == "db" && method != "Query" && method != "QueryRow" {
					t.Errorf("%s: %s calls db.%s; read methods may only Query/QueryRow", file, fd.Name.Name, method)
				}
				return true
			})
		}
		for fn := range want {
			if !found[fn] {
				t.Errorf("%s: function %s not found; update downloadCode in this test", file, fn)
			}
		}
	}
}

// receiverName returns the last identifier of a selector's receiver: s.messageStore → "messageStore"
func receiverName(x ast.Expr) string {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

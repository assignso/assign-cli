// Offline oracle using the frozen CLI generated Core serializer.
package main
import (
 "encoding/json"
 "os"
 rich "github.com/assignso/assign-cli/internal/generated/richtext"
)
func main() {
 var cases []map[string]any
 for _, input := range []string{"Hello **bold** and *italic*.\n", "> [!NOTE]\n> Body\n", "[go](https://example.com/a)\n", "````ts\n```\nx\n````\n", "| H |\n| --- |\n| A |\n", "- first\n- second\n", "1. first\n2. next\n", "> quote\n", "# Heading\n\n---\n", "++under++ and ~~strike~~ and `code`\n"} {
  raw,err:=rich.FromMarkdown([]byte(input)); if err!=nil {panic(err)}
  rendered,err:=rich.ToMarkdown(raw); if err!=nil {panic(err)}
  cases=append(cases,map[string]any{"input":json.RawMessage(raw),"output":string(rendered)})
 }
 encoder:=json.NewEncoder(os.Stdout); encoder.SetIndent("","  "); if err:=encoder.Encode(cases);err!=nil {panic(err)}
}

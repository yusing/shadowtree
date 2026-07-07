package scriptref

import "testing"

func TestParseFindsCommandPositionReferences(t *testing.T) {
	_, refs, err := Parse("sh", "if @check; then\n  FOO=bar @build mode=dev\nfi\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %#v, want two", refs)
	}
	if refs[0].Value != "@check" || refs[0].Start.Line != 0 || refs[0].Start.Col != len("if ") {
		t.Fatalf("refs[0] = %#v, want @check after if", refs[0])
	}
	if refs[1].Value != "@build" || refs[1].Start.Line != 1 || refs[1].Start.Col != len("  FOO=bar ") {
		t.Fatalf("refs[1] = %#v, want indented @build", refs[1])
	}
	if int(refs[1].CommandPos.Line())-1 != refs[1].Start.Line || int(refs[1].CommandPos.Col())-1 != refs[1].Start.Col {
		t.Fatalf("refs[1].CommandPos = %v, want target start %#v", refs[1].CommandPos, refs[1].Start)
	}
}

func TestParseIgnoresNonCommandReferences(t *testing.T) {
	_, refs, err := Parse("sh", `FOO="@missing"
echo @also_missing
cat <<EOF
@heredoc
EOF
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %#v, want none", refs)
	}
}

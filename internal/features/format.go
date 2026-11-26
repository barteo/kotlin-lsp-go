package features

import (
	"github.com/barteo/kotlin-lsp-go/internal/format"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// FormatOptions returns the formatter's options for the file at path:
// the client's indentation, then what the .editorconfig files say.
func FormatOptions(path string, o protocol.FormattingOptions) format.Options {
	fo := format.Default()
	if o.TabSize > 0 {
		fo.IndentSize, fo.TabWidth = int(o.TabSize), int(o.TabSize)
	}
	fo.UseTabs = !o.InsertSpaces && o.TabSize > 0
	return format.EditorConfig(path, fo)
}

// Formatting returns the edits that format f.
func Formatting(f *File, o format.Options) []protocol.TextEdit {
	return textEdits(f, format.Format(f.Snapshot, o))
}

// RangeFormatting returns the edits that format the range r of f.
func RangeFormatting(f *File, r protocol.Range, o format.Options) []protocol.TextEdit {
	start, end, err := f.Mapper.OffsetRange(r)
	if err != nil {
		return []protocol.TextEdit{}
	}
	return textEdits(f, format.Range(f.Snapshot, o, start, end))
}

// OnTypeFormatting returns the edits that format what typing ch before
// pos ended: a block, a statement, or a line.
func OnTypeFormatting(f *File, pos protocol.Position, ch string, o format.Options) []protocol.TextEdit {
	off, err := f.Mapper.Offset(pos)
	if err != nil {
		return []protocol.TextEdit{}
	}
	return textEdits(f, format.OnType(f.Snapshot, o, off, ch))
}

func textEdits(f *File, edits []format.Edit) []protocol.TextEdit {
	out := []protocol.TextEdit{}
	for _, e := range edits {
		out = append(out, protocol.TextEdit{Range: f.Mapper.Range(e.Start, e.End), NewText: e.Text})
	}
	return out
}

package format

import (
	"strings"

	"github.com/barteo/kotlin-compiler-go/psi"
)

// space returns the spacing between tokens a and b on one line, which
// orig separates now: none or one space by the Kotlin conventions, or
// orig where they say nothing.
func (f *formatter) space(a, b psi.NodeRef, orig string) string {
	ka, kb := a.NumericKind(), b.NumericKind()
	pa, pb := a.Parent(), b.Parent()
	if comment(ka) || comment(kb) {
		if orig == "" && kb == psi.KindEOL_COMMENT {
			return " "
		}
		return orig
	}
	switch {
	case kb == psi.KindCOMMA || kb == psi.KindSEMICOLON:
		return ""
	case ka == psi.KindCOMMA:
		if closing(b) {
			return ""
		}
		return " "
	case ka == psi.KindSEMICOLON:
		return " "
	case ka == psi.KindDOT || kb == psi.KindDOT || ka == psi.KindSAFE_ACCESS || kb == psi.KindSAFE_ACCESS:
		return ""
	case ka == psi.KindCOLONCOLON:
		return ""
	case kb == psi.KindCOLONCOLON && prevSibling(b).Valid():
		return ""
	case ka == psi.KindLPAR || ka == psi.KindLBRACKET:
		return ""
	case kb == psi.KindRPAR || kb == psi.KindRBRACKET:
		return ""
	case kb == psi.KindLPAR:
		switch pb.NumericKind() {
		case psi.KindVALUE_ARGUMENT_LIST:
			return ""
		case psi.KindVALUE_PARAMETER_LIST:
			switch pb.Parent().NumericKind() {
			case psi.KindFUN, psi.KindPRIMARY_CONSTRUCTOR, psi.KindSECONDARY_CONSTRUCTOR, psi.KindPROPERTY_ACCESSOR:
				return ""
			case psi.KindCATCH:
				return " "
			}
		case psi.KindIF, psi.KindWHILE, psi.KindFOR, psi.KindWHEN, psi.KindDO_WHILE:
			return " "
		}
		if keyword(a) || binaryOperator(a) || ka == psi.KindEQ || ka == psi.KindARROW || ka == psi.KindCOLON {
			return " "
		}
		return orig
	case kb == psi.KindLBRACKET && pb.NumericKind() == psi.KindINDICES:
		return ""
	case kb == psi.KindQUEST && pb.NumericKind() == psi.KindNULLABLE_TYPE:
		return ""
	case kb == psi.KindLT && typeList(pb):
		if ka == psi.KindFUN_KEYWORD {
			return " "
		}
		return ""
	case ka == psi.KindLT && typeList(pa), kb == psi.KindGT && typeList(pb):
		return ""
	case kb == psi.KindLT && ka == psi.KindSUPER_KEYWORD:
		return ""
	case ka == psi.KindGT && typeList(pa):
		return " "
	case operator(b, psi.KindPOSTFIX_EXPRESSION):
		return ""
	case operator(a, psi.KindPREFIX_EXPRESSION):
		// - -x isn't --x.
		if t := a.Text(); (t == "-" || t == "+") && strings.HasPrefix(f.src[b.Range().Start:], t) {
			return " "
		}
		return ""
	case binaryOperator(a) || binaryOperator(b):
		op := a
		if binaryOperator(b) {
			op = b
		}
		if k := op.NumericKind(); k == psi.KindRANGE || k == psi.KindRANGE_UNTIL {
			return ""
		}
		return " "
	case ka == psi.KindEQ || kb == psi.KindEQ || ka == psi.KindARROW || kb == psi.KindARROW:
		return " "
	case kb == psi.KindCOLON:
		switch pb.NumericKind() {
		case psi.KindVALUE_PARAMETER, psi.KindPROPERTY, psi.KindFUN, psi.KindDESTRUCTURING_DECLARATION_ENTRY, psi.KindPROPERTY_ACCESSOR:
			return ""
		case psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindTYPE_PARAMETER, psi.KindTYPE_CONSTRAINT, psi.KindSECONDARY_CONSTRUCTOR,
			psi.KindENUM_ENTRY:
			return " "
		case psi.KindANNOTATION_ENTRY, psi.KindANNOTATION:
			return ""
		}
		return orig
	case ka == psi.KindCOLON:
		switch pa.NumericKind() {
		case psi.KindANNOTATION_ENTRY, psi.KindANNOTATION:
			return ""
		case psi.KindVALUE_PARAMETER, psi.KindPROPERTY, psi.KindFUN, psi.KindDESTRUCTURING_DECLARATION_ENTRY, psi.KindPROPERTY_ACCESSOR,
			psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindTYPE_PARAMETER, psi.KindTYPE_CONSTRAINT, psi.KindSECONDARY_CONSTRUCTOR,
			psi.KindENUM_ENTRY:
			return " "
		}
		return orig
	case ka == psi.KindAT:
		// @Annotation, @label; loop@ for, but foo bar@{ }.
		if pa.NumericKind() == psi.KindLABEL && !a.NextSibling().Valid() && kb != psi.KindLBRACE {
			return " "
		}
		return ""
	case kb == psi.KindAT && pb.NumericKind() == psi.KindLABEL:
		return ""
	case ka == psi.KindLBRACE && kb == psi.KindRBRACE:
		if orig != "" {
			return " "
		}
		return ""
	case kb == psi.KindLBRACE, ka == psi.KindLBRACE, kb == psi.KindRBRACE, ka == psi.KindRBRACE:
		return " "
	case ka == psi.KindMUL && pa.NumericKind() == psi.KindVALUE_ARGUMENT:
		return "" // a spread
	case wordy(a) || wordy(b):
		if wordy(a) && wordy(b) || keyword(a) || keyword(b) {
			return " "
		}
	}
	return orig
}

func comment(k psi.Kind) bool {
	return k == psi.KindEOL_COMMENT || k == psi.KindBLOCK_COMMENT || k == psi.KindKDOC || k == psi.KindSHEBANG_COMMENT
}

func closing(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindRPAR, psi.KindRBRACKET, psi.KindRBRACE:
		return true
	case psi.KindGT:
		return typeList(n.Parent())
	}
	return false
}

func prevSibling(n psi.NodeRef) psi.NodeRef {
	for c := n.PreviousSibling(); c.Valid(); c = c.PreviousSibling() {
		if !c.IsTrivia() {
			return c
		}
	}
	return psi.NodeRef{}
}

// operator reports whether n is the operator of an expression of kind k.
func operator(n psi.NodeRef, k psi.Kind) bool {
	p := n.Parent()
	return p.NumericKind() == psi.KindOPERATION_REFERENCE && p.Parent().NumericKind() == k
}

func binaryOperator(n psi.NodeRef) bool {
	return operator(n, psi.KindBINARY_EXPRESSION) || operator(n, psi.KindBINARY_WITH_TYPE) || operator(n, psi.KindIS_EXPRESSION)
}

func keyword(n psi.NodeRef) bool {
	k := n.NumericKind()
	return psi.IsKeyword(k) || strings.HasSuffix(n.Kind(), "_KEYWORD")
}

// wordy reports whether n is a word: a name, a keyword or a literal,
// which another word must be spaced from.
func wordy(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindIDENTIFIER, psi.KindINTEGER_LITERAL, psi.KindFLOAT_LITERAL, psi.KindCHARACTER_LITERAL, psi.KindSTRING_TEMPLATE:
		return true
	}
	return keyword(n)
}

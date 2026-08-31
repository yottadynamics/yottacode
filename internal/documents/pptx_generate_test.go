package documents

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPNGPath writes a minimal valid 1x1 PNG to a temp file and returns its
// path — GeneratePPTX decodes the image to validate it, so a fixture that
// isn't real image bytes fails before geometry is even relevant.
func testPNGPath(t *testing.T) string {
	t.Helper()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatalf("write test png: %v", err)
	}
	return path
}

// pptxSlide1XML generates a one-slide deck and returns ppt/slides/slide1.xml's
// raw bytes, so tests can assert on the exact <a:off>/<a:ext> geometry
// GeneratePPTX wrote without re-implementing an OOXML parser.
func pptxSlide1XML(t *testing.T, slide SlideModel) string {
	t.Helper()
	data, err := GeneratePPTX([]SlideModel{slide})
	if err != nil {
		t.Fatalf("GeneratePPTX: %v", err)
	}
	zr, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		t.Fatalf("open generated pptx: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == "ppt/slides/slide1.xml" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open slide1.xml: %v", err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read slide1.xml: %v", err)
			}
			return string(b)
		}
	}
	t.Fatalf("generated pptx has no ppt/slides/slide1.xml")
	return ""
}

// TestGeneratePPTXImageDefaultBoundsUnchanged pins that a nil ImageBounds
// reproduces the exact fixed right-half geometry that existed before this
// field was added — the backward-compatibility contract issue #7 required.
func TestGeneratePPTXImageDefaultBoundsUnchanged(t *testing.T) {
	xml := pptxSlide1XML(t, SlideModel{Title: "x", Image: testPNGPath(t)})
	wantOff := `<a:off x="6553200" y="1463040"/>`
	wantExt := `<a:ext cx="5029200" cy="3429000"/>`
	if !strings.Contains(xml, wantOff) {
		t.Errorf("default image offset changed: want %q in\n%s", wantOff, xml)
	}
	if !strings.Contains(xml, wantExt) {
		t.Errorf("default image extent changed: want %q in\n%s", wantExt, xml)
	}
}

// TestGeneratePPTXImageExplicitBounds confirms an explicit ImageBounds is
// rendered verbatim into the picture shape's <a:xfrm>.
func TestGeneratePPTXImageExplicitBounds(t *testing.T) {
	bounds := &ImageBounds{X: 100, Y: 200, CX: 300, CY: 400}
	xml := pptxSlide1XML(t, SlideModel{Image: testPNGPath(t), ImageBounds: bounds})
	wantOff := `<a:off x="100" y="200"/>`
	wantExt := `<a:ext cx="300" cy="400"/>`
	if !strings.Contains(xml, wantOff) {
		t.Errorf("expected explicit offset %q in\n%s", wantOff, xml)
	}
	if !strings.Contains(xml, wantExt) {
		t.Errorf("expected explicit extent %q in\n%s", wantExt, xml)
	}
}

// TestGeneratePPTXNoImageNoBoundsPanic guards against a nil-pointer
// dereference when ImageBounds is set on a slide with no image — the field
// must simply be ignored, not read, since Image == "" means pptxPictureShape
// is never called at all.
func TestGeneratePPTXNoImageNoBoundsPanic(t *testing.T) {
	xml := pptxSlide1XML(t, SlideModel{Title: "no image here", ImageBounds: &ImageBounds{X: 1, Y: 1, CX: 1, CY: 1}})
	if strings.Contains(xml, "<p:pic>") {
		t.Errorf("expected no picture shape when Image is empty, got:\n%s", xml)
	}
}

func TestSlideDimensionConstantsMatchPresentationXML(t *testing.T) {
	want := fmt.Sprintf(`<p:sldSz cx="%d" cy="%d" type="wide"/>`, SlideWidthEMU, SlideHeightEMU)
	got := pptxPresentationXML(1)
	if !strings.Contains(got, want) {
		t.Errorf("pptxPresentationXML doesn't use the exported SlideWidthEMU/SlideHeightEMU constants: want %q in %q", want, got)
	}
}

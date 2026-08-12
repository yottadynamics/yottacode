package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SlideModel is one slide in a generated PPTX deck. It intentionally
// supports a small, predictable subset of PowerPoint: title, bullets,
// speaker notes, and one optional image. That is enough for the agent's
// current structured create_document schema while keeping generation pure Go.
type SlideModel struct {
	Title    string
	Bullets  []string
	Notes    string
	Image    string
	ImageAlt string
	Layout   string
}

// GeneratePPTX renders slides into a minimal Office Open XML presentation.
// It is pure Go: no python3, python-pptx, LibreOffice, or sandbox process is
// required. The generated package includes the standard presentation parts,
// slide XML, optional note slides, and optional image relationships.
func GeneratePPTX(slides []SlideModel) ([]byte, error) {
	if len(slides) == 0 {
		return nil, fmt.Errorf("pptx: at least one slide is required")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writePPTXStaticParts(zw, slides); err != nil {
		zw.Close()
		return nil, err
	}
	for i, slide := range slides {
		if err := writePPTXSlide(zw, i+1, slide); err != nil {
			zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("pptx: close archive: %w", err)
	}
	return buf.Bytes(), nil
}

func writePPTXStaticParts(zw *zip.Writer, slides []SlideModel) error {
	parts := map[string]string{
		"_rels/.rels":                                  pptxRootRels,
		"docProps/app.xml":                             pptxAppXML(len(slides)),
		"docProps/core.xml":                            pptxCoreXML,
		"ppt/presProps.xml":                            pptxPresPropsXML,
		"ppt/viewProps.xml":                            pptxViewPropsXML,
		"ppt/tableStyles.xml":                          pptxTableStylesXML,
		"ppt/theme/theme1.xml":                         pptxThemeXML,
		"ppt/slideMasters/slideMaster1.xml":            pptxSlideMasterXML,
		"ppt/slideMasters/_rels/slideMaster1.xml.rels": pptxSlideMasterRelsXML,
		"ppt/slideLayouts/slideLayout1.xml":            pptxSlideLayoutXML,
		"ppt/slideLayouts/_rels/slideLayout1.xml.rels": pptxSlideLayoutRelsXML,
		"ppt/presentation.xml":                         pptxPresentationXML(len(slides)),
		"ppt/_rels/presentation.xml.rels":              pptxPresentationRelsXML(slides),
		"[Content_Types].xml":                          pptxContentTypesXML(slides),
	}
	for name, body := range parts {
		if err := zipWriteString(zw, name, body); err != nil {
			return err
		}
	}
	return nil
}

func writePPTXSlide(zw *zip.Writer, index int, slide SlideModel) error {
	if err := zipWriteString(zw, fmt.Sprintf("ppt/slides/slide%d.xml", index), pptxSlideXML(slide)); err != nil {
		return err
	}
	if err := zipWriteString(zw, fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", index), pptxSlideRelsXML(index, slide)); err != nil {
		return err
	}
	if strings.TrimSpace(slide.Notes) != "" {
		if err := zipWriteString(zw, fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", index), pptxNotesSlideXML(slide.Notes)); err != nil {
			return err
		}
		if err := zipWriteString(zw, fmt.Sprintf("ppt/notesSlides/_rels/notesSlide%d.xml.rels", index), pptxNotesSlideRelsXML(index)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(slide.Image) != "" {
		data, ext, err := readPPTXImage(slide.Image)
		if err != nil {
			return fmt.Errorf("pptx: slide %d image: %w", index, err)
		}
		if err := zipWriteBytes(zw, fmt.Sprintf("ppt/media/image%d.%s", index, ext), data); err != nil {
			return err
		}
	}
	return nil
}

func readPPTXImage(path string) ([]byte, string, error) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "jpeg" {
		ext = "jpg"
	}
	if ext != "png" && ext != "jpg" && ext != "gif" {
		return nil, "", fmt.Errorf("unsupported image extension %q (supported: .png, .jpg, .jpeg, .gif)", filepath.Ext(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", fmt.Errorf("image has invalid dimensions")
	}
	if format == "jpeg" {
		format = "jpg"
	}
	if format != ext {
		return nil, "", fmt.Errorf("image extension .%s does not match decoded %s data", ext, format)
	}
	return data, ext, nil
}

func zipWriteString(zw *zip.Writer, name, body string) error {
	return zipWriteBytes(zw, name, []byte(body))
}

func zipWriteBytes(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("pptx: create %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("pptx: write %s: %w", name, err)
	}
	return nil
}

func pptxContentTypesXML(slides []SlideModel) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	imageDefaults := map[string]bool{}
	for _, slide := range slides {
		if slide.Image == "" {
			continue
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(slide.Image)), ".")
		if ext == "jpeg" {
			ext = "jpg"
		}
		if ext != "" {
			imageDefaults[ext] = true
		}
	}
	for ext := range imageDefaults {
		contentType := "image/" + ext
		if ext == "jpg" {
			contentType = "image/jpeg"
		}
		b.WriteString(fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, ext, contentType))
	}
	b.WriteString(`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/tableStyles.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>`)
	for i, slide := range slides {
		b.WriteString(fmt.Sprintf(`<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i+1))
		if strings.TrimSpace(slide.Notes) != "" {
			b.WriteString(fmt.Sprintf(`<Override PartName="/ppt/notesSlides/notesSlide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>`, i+1))
		}
	}
	b.WriteString(`</Types>`)
	return b.String()
}

const pptxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`

const pptxCoreXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>yottacode presentation</dc:title><dc:creator>yottacode</dc:creator><cp:lastModifiedBy>yottacode</cp:lastModifiedBy></cp:coreProperties>`

func pptxAppXML(slideCount int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>yottacode</Application><PresentationFormat>Widescreen</PresentationFormat><Slides>%d</Slides></Properties>`, slideCount)
}

func pptxPresentationXML(slideCount int) string {
	var ids strings.Builder
	for i := 1; i <= slideCount; i++ {
		ids.WriteString(fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, 255+i, i))
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId` + fmt.Sprint(slideCount+1) + `"/></p:sldMasterIdLst><p:sldIdLst>` + ids.String() + `</p:sldIdLst><p:sldSz cx="12192000" cy="6858000" type="wide"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`
}

func pptxPresentationRelsXML(slides []SlideModel) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range slides {
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, i+1, i+1))
	}
	n := len(slides)
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`, n+1))
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>`, n+2))
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/presProps" Target="presProps.xml"/>`, n+3))
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/viewProps" Target="viewProps.xml"/>`, n+4))
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/tableStyles" Target="tableStyles.xml"/>`, n+5))
	b.WriteString(`</Relationships>`)
	return b.String()
}

const pptxSlideMasterXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree></p:cSld><p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst><p:txStyles><p:titleStyle/><p:bodyStyle/><p:otherStyle/></p:txStyles></p:sldMaster>`
const pptxSlideMasterRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`
const pptxSlideLayoutXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="blank" preserve="1"><p:cSld name="Blank"><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree></p:cSld></p:sldLayout>`
const pptxSlideLayoutRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`
const pptxThemeXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="yottacode"><a:themeElements><a:clrScheme name="yottacode"><a:dk1><a:srgbClr val="0B0F0D"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="1F2937"/></a:dk2><a:lt2><a:srgbClr val="F8FAFC"/></a:lt2><a:accent1><a:srgbClr val="22C55E"/></a:accent1><a:accent2><a:srgbClr val="16A34A"/></a:accent2><a:accent3><a:srgbClr val="86EFAC"/></a:accent3><a:accent4><a:srgbClr val="052E16"/></a:accent4><a:accent5><a:srgbClr val="4ADE80"/></a:accent5><a:accent6><a:srgbClr val="BBF7D0"/></a:accent6><a:hlink><a:srgbClr val="16A34A"/></a:hlink><a:folHlink><a:srgbClr val="15803D"/></a:folHlink></a:clrScheme><a:fontScheme name="yottacode"><a:majorFont><a:latin typeface="Arial"/></a:majorFont><a:minorFont><a:latin typeface="Arial"/></a:minorFont></a:fontScheme><a:fmtScheme name="yottacode"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst><a:lnStyleLst><a:ln w="9525"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`
const pptxPresPropsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentationPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`
const pptxViewPropsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:viewPr xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`
const pptxTableStylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:tblStyleLst xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" def="{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}"/>`

func pptxSlideXML(slide SlideModel) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>`)
	shapeID := 2
	if strings.TrimSpace(slide.Title) != "" {
		b.WriteString(pptxTextShape(shapeID, "Title", slide.Title, nil, 457200, 274320, 11277600, 914400, 3600, true))
		shapeID++
	}
	if len(slide.Bullets) > 0 {
		b.WriteString(pptxTextShape(shapeID, "Bullets", "", slide.Bullets, 914400, 1463040, 10363200, 3429000, 2400, false))
		shapeID++
	}
	if strings.TrimSpace(slide.Image) != "" {
		b.WriteString(pptxPictureShape(shapeID, slide.ImageAlt))
	}
	b.WriteString(`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`)
	return b.String()
}

func pptxTextShape(id int, name, title string, bullets []string, x, y, cx, cy, fontSize int, bold bool) string {
	var paras strings.Builder
	if len(bullets) == 0 {
		paras.WriteString(pptxParagraph(title, false, fontSize, bold))
	} else {
		for _, item := range bullets {
			paras.WriteString(pptxParagraph(item, true, fontSize, bold))
		}
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/></p:spPr><p:txBody><a:bodyPr wrap="square"/><a:lstStyle/>%s</p:txBody></p:sp>`, id, xmlEsc(name), x, y, cx, cy, paras.String())
}

func pptxParagraph(text string, bullet bool, fontSize int, bold bool) string {
	bu := ""
	if bullet {
		bu = `<a:buChar char="•"/>`
	}
	boldAttr := "0"
	if bold {
		boldAttr = "1"
	}
	return fmt.Sprintf(`<a:p><a:pPr marL="342900" indent="-171450">%s</a:pPr><a:r><a:rPr lang="en-US" sz="%d" b="%s"/><a:t>%s</a:t></a:r><a:endParaRPr lang="en-US" sz="%d"/></a:p>`, bu, fontSize, boldAttr, xmlEsc(text), fontSize)
}

func pptxPictureShape(id int, alt string) string {
	name := alt
	if strings.TrimSpace(name) == "" {
		name = "Picture"
	}
	// Images are placed on the right half of a widescreen slide. Text still
	// remains readable for ordinary title+bullets+image decks; precise layout
	// control can be added later without changing the public schema.
	return fmt.Sprintf(`<p:pic><p:nvPicPr><p:cNvPr id="%d" name="%s" descr="%s"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr><p:blipFill><a:blip r:embed="rId2"/><a:stretch><a:fillRect/></a:stretch></p:blipFill><p:spPr><a:xfrm><a:off x="6553200" y="1463040"/><a:ext cx="5029200" cy="3429000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr></p:pic>`, id, xmlEsc(name), xmlEsc(alt))
}

func pptxSlideRelsXML(index int, slide SlideModel) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	b.WriteString(`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>`)
	nextID := 2
	if strings.TrimSpace(slide.Image) != "" {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(slide.Image)), ".")
		if ext == "jpeg" {
			ext = "jpg"
		}
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image%d.%s"/>`, nextID, index, xmlEsc(ext)))
		nextID++
	}
	if strings.TrimSpace(slide.Notes) != "" {
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide%d.xml"/>`, nextID, index))
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

func pptxNotesSlideXML(notes string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:notes xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>` + pptxTextShape(2, "Notes", notes, nil, 914400, 914400, 5029200, 6858000, 1800, false) + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
}

func pptxNotesSlideRelsXML(index int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="../slides/slide%d.xml"/></Relationships>`, index)
}

func xmlEsc(s string) string {
	return html.EscapeString(s)
}

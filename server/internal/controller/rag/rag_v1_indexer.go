package rag

import (
	"context"
	"path"
	"strings"

	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/frame/g"
	gorag "github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/internal/logic/knowledge"
	"github.com/wangle201210/go-rag/server/internal/logic/rag"
	"github.com/wangle201210/go-rag/server/internal/model/entity"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
)

// validateIndexerInput checks File/URL parameters before any side effects.
// Returns error if neither File nor URL is provided, or if PaperDesk mode rejects the input.
func validateIndexerInput(req *v1.IndexerReq) error {
	hasFile := req.File != nil && req.File.Filename != ""
	hasURL := strings.TrimSpace(req.URL) != ""

	if !hasFile && !hasURL {
		return gerror.NewCode(gcode.CodeMissingParameter, "either file or URL is required")
	}

	// PaperDesk mode: only File upload is accepted; URL import is disabled.
	if isPaperDeskMode() {
		if hasURL && !hasFile {
			return gerror.NewCode(gcode.CodeInvalidParameter, "PaperDesk mode does not accept URL import")
		}
		if hasURL && hasFile {
			return gerror.NewCode(gcode.CodeInvalidParameter, "PaperDesk mode only accepts file upload, URL is not allowed")
		}
	}

	return nil
}

// paperDeskModeOverride allows tests to override PaperDesk mode detection.
// nil → use default (false). Tests may set this to a function returning true/false.
var paperDeskModeOverride func() bool

// isPaperDeskMode reports whether the server runs in PaperDesk profile.
// TODO: When PaperDesk profile configuration is implemented, read from actual config.
// Temporary strategy: PaperDesk mode is not enabled.
func isPaperDeskMode() bool {
	if paperDeskModeOverride != nil {
		return paperDeskModeOverride()
	}
	return false
}

func (c *ControllerV1) Indexer(ctx context.Context, req *v1.IndexerReq) (res *v1.IndexerRes, err error) {
	// Parameter validation before any side effects (file save, DB write, indexing, task start).
	if err = validateIndexerInput(req); err != nil {
		return nil, err
	}

	svr := rag.GetRagSvr()
	uri := req.URL
	fileName := ""
	if req.File != nil && req.File.Filename != "" {
		filename, e := req.File.Save("./uploads/")
		if e != nil {
			err = e
			return
		}
		uri = "./uploads/" + filename
		fileName = req.File.Filename
	} else if strings.TrimSpace(req.URL) != "" {
		// URL mode: derive a display name from the URL path for the document record.
		fileName = path.Base(req.URL)
	}

	documents := entity.KnowledgeDocuments{
		KnowledgeBaseName: req.KnowledgeName,
		FileName:          fileName,
		Status:            int(v1.StatusPending),
	}
	documentsId, err := knowledge.SaveDocumentsInfo(ctx, documents)
	if err != nil {
		g.Log().Errorf(ctx, "SaveDocumentsInfo failed, err=%v", err)
		return
	}

	indexReq := &gorag.IndexReq{
		URI:           uri,
		KnowledgeName: req.KnowledgeName,
		DocumentsId:   documentsId,
	}
	ids, err := svr.Index(ctx, indexReq)
	if err != nil {
		return
	}
	res = &v1.IndexerRes{
		DocIDs: ids,
	}
	return
}

package actionflow

import "encoding/json"

type storedFile struct {
	FileName string `json:"fileName"`
	FileID   string `json:"fileId"`
}

func writtenOryxaFile(raw json.RawMessage) (filename, fileID string) {
	var value struct {
		storedFile
		Name string     `json:"name"`
		Data storedFile `json:"data"`
		File storedFile `json:"file"`
	}
	_ = json.Unmarshal(raw, &value)
	fileID = firstNonEmpty(value.FileID, value.Data.FileID, value.File.FileID)
	filename = firstNonEmpty(value.FileName, value.Data.FileName, value.File.FileName, value.Name)
	return filename, fileID
}

func mapStoredFileLookup(raw json.RawMessage) (MappedResult, error) {
	filename, fileID := writtenOryxaFile(raw)
	if fileID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		FileID   string `json:"file_id"`
		Filename string `json:"filename,omitempty"`
	}{FileID: fileID, Filename: filename})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

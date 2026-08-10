package agentrpc

import (
	"context"
	"time"

	agentv1 "github.com/gugumanager/gugumanager/api/proto/gugumanager/agent/v1"
	"github.com/gugumanager/gugumanager/internal/domain"
	"github.com/gugumanager/gugumanager/internal/store"
)

// 编译期断言：*Server 实现 store.FileDispatcher。
var _ store.FileDispatcher = (*Server)(nil)

// ListFiles 列出服务器容器 /data 下指定目录的直接子项。
func (s *Server) ListFiles(ctx context.Context, nodeID, serverID, path string) ([]domain.FileEntry, error) {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_List{
			List: &agentv1.FileOperationRequest_ListFilesInput{Path: path},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return nil, err
	}
	if !resp.GetSucceeded() {
		return nil, &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	listResult := resp.GetList()
	if listResult == nil {
		return nil, &store.AgentFileError{Code: "INTERNAL_ERROR"}
	}
	entries := make([]domain.FileEntry, 0, len(listResult.GetEntries()))
	for _, e := range listResult.GetEntries() {
		kind := "file"
		if e.GetDirectory() {
			kind = "directory"
		}
		var modTime time.Time
		if ts := e.GetModifiedAt(); ts != nil {
			modTime = ts.AsTime()
		}
		entries = append(entries, domain.FileEntry{
			Name:       e.GetName(),
			Path:       e.GetPath(),
			Kind:       kind,
			SizeBytes:  int64(e.GetSizeBytes()),
			ModifiedAt: modTime,
		})
	}
	return entries, nil
}

// ReadFile 读取服务器容器 /data 下的单个文件内容。
func (s *Server) ReadFile(ctx context.Context, nodeID, serverID, path string) (domain.FileContent, error) {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_Read{
			Read: &agentv1.FileOperationRequest_ReadFileInput{Path: path},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return domain.FileContent{}, err
	}
	if !resp.GetSucceeded() {
		return domain.FileContent{}, &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	readResult := resp.GetRead()
	if readResult == nil {
		return domain.FileContent{}, &store.AgentFileError{Code: "INTERNAL_ERROR"}
	}
	encoding := "utf-8"
	if readResult.GetBase64() {
		encoding = "base64"
	}
	var modTime time.Time
	if ts := readResult.GetModifiedAt(); ts != nil {
		modTime = ts.AsTime()
	}
	return domain.FileContent{
		Path:       path,
		Content:    string(readResult.GetContent()),
		Encoding:   encoding,
		SizeBytes:  int64(readResult.GetSizeBytes()),
		ModifiedAt: modTime,
	}, nil
}

// WriteFile 写入文件到服务器容器 /data 目录。
func (s *Server) WriteFile(ctx context.Context, nodeID, serverID, path string, content []byte, base64 bool) error {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_Write{
			Write: &agentv1.FileOperationRequest_WriteFileInput{
				Path:    path,
				Content: content,
				Base64:  base64,
			},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return err
	}
	if !resp.GetSucceeded() {
		return &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	return nil
}

// MakeDirectory 在服务器容器 /data 下创建目录。
func (s *Server) MakeDirectory(ctx context.Context, nodeID, serverID, path string) error {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_Mkdir{
			Mkdir: &agentv1.FileOperationRequest_MakeDirectoryInput{Path: path},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return err
	}
	if !resp.GetSucceeded() {
		return &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	return nil
}

// MoveFile 在服务器容器 /data 下移动或重命名文件/目录。
func (s *Server) MoveFile(ctx context.Context, nodeID, serverID, source, destination string, replace bool) error {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_Move{
			Move: &agentv1.FileOperationRequest_MoveFileInput{
				Source:      source,
				Destination: destination,
				Replace:     replace,
			},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return err
	}
	if !resp.GetSucceeded() {
		return &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	return nil
}

// RemoveFile 在服务器容器 /data 下删除文件或目录树。
func (s *Server) RemoveFile(ctx context.Context, nodeID, serverID, path string, recursive bool) error {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_Remove{
			Remove: &agentv1.FileOperationRequest_RemoveFileInput{
				Path:      path,
				Recursive: recursive,
			},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return err
	}
	if !resp.GetSucceeded() {
		return &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	return nil
}

// DownloadBackup 从节点本地备份目录读取备份归档并回传内容。
func (s *Server) DownloadBackup(ctx context.Context, nodeID, serverID, backupID string) (domain.BackupContent, error) {
	req := &agentv1.FileOperationRequest{
		ServerId: serverID,
		Operation: &agentv1.FileOperationRequest_DownloadBackup{
			DownloadBackup: &agentv1.FileOperationRequest_DownloadBackupInput{BackupId: backupID},
		},
	}
	resp, err := s.DispatchFileOperation(ctx, nodeID, req)
	if err != nil {
		return domain.BackupContent{}, err
	}
	if !resp.GetSucceeded() {
		return domain.BackupContent{}, &store.AgentFileError{Code: resp.GetErrorCode()}
	}
	result := resp.GetDownloadBackup()
	if result == nil {
		return domain.BackupContent{}, &store.AgentFileError{Code: "INTERNAL_ERROR"}
	}
	return domain.BackupContent{
		Content:   result.GetContent(),
		Base64:    result.GetBase64(),
		SizeBytes: int64(result.GetSizeBytes()),
		Filename:  result.GetFilename(),
	}, nil
}

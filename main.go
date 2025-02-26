package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	// 路由配置
	http.HandleFunc("/download", Download)
	http.HandleFunc("/upload", Upload)

	if err := http.ListenAndServe(":11451", nil); err != nil {
		fmt.Println("Error starting server:", err)
	}
}
func Download(w http.ResponseWriter, r *http.Request) {
	// 从 URL 获取参数
	downloadURL := r.URL.Query().Get("url")

	// 参数检查
	if downloadURL == "" {
		http.Error(w, "Missing required parameters", http.StatusBadRequest)
		return
	}

	// 调用下载函数
	err := DownloadFile(downloadURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error downloading file: %v", err), http.StatusInternalServerError)
		return
	}

	// 下载完成，响应成功
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"status\":true}"))
}

const chunkSize = 50 * 1024 * 1024 // 50MB

func Upload(w http.ResponseWriter, r *http.Request) {
	// 解析表单，获取文件
	err := r.ParseMultipartForm(0) // 使用 0 让文件存储到磁盘
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	// 获取上传的文件
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error reading file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 获取文件的原始名称
	originalFileName := fileHeader.Filename

	// 创建存储文件夹
	uploadDir := "./temp"
	err = os.MkdirAll(uploadDir, 0755)
	if err != nil {
		http.Error(w, "Error creating directory", http.StatusInternalServerError)
		return
	}

	// 创建记录MD5值的文件
	md5File, err := os.Create(filepath.Join(uploadDir, "links.txt"))
	if err != nil {
		http.Error(w, "Error creating MD5 record file", http.StatusInternalServerError)
		return
	}
	defer md5File.Close()

	// 写入原始文件名到MD5记录文件
	_, err = md5File.Write([]byte(originalFileName + "\n"))
	if err != nil {
		http.Error(w, "Error writing to MD5 record file", http.StatusInternalServerError)
		return
	}

	// 处理文件分割
	buffer := make([]byte, chunkSize)
	var chunkNum int
	for {
		n, err := file.Read(buffer)
		if err == io.EOF {
			break // 文件读完
		}
		if err != nil {
			http.Error(w, "Error reading file", http.StatusInternalServerError)
			return
		}

		// 创建临时文件来保存分割内容
		tempChunkFileName := fmt.Sprintf("%s/temp_chunk_%d", uploadDir, chunkNum)
		chunkFile, err := os.Create(tempChunkFileName)
		if err != nil {
			http.Error(w, "Error creating chunk file", http.StatusInternalServerError)
			return
		}

		// 写入分割内容
		_, err = chunkFile.Write(buffer[:n])
		if err != nil {
			http.Error(w, "Error writing to chunk file", http.StatusInternalServerError)
			chunkFile.Close() // 确保文件被关闭
			return
		}

		// 关闭文件后再计算MD5和重命名
		chunkFile.Close()

		// 计算分割文件的MD5
		chunkFileMD5 := md5.New()
		chunkFileMD5.Write(buffer[:n])
		md5Sum := chunkFileMD5.Sum(nil)
		md5Hex := fmt.Sprintf("%x", md5Sum)

		// 重命名临时文件为MD5值
		newChunkFileName := fmt.Sprintf("%s/%s.%s", uploadDir, md5Hex, "zip")
		err = os.Rename(tempChunkFileName, newChunkFileName)
		if err != nil {
			http.Error(w, "Error renaming chunk file", http.StatusInternalServerError)
			return
		}

		// 记录MD5到文件
		_, err = md5File.Write([]byte(fmt.Sprintf("%s\n", md5Hex)))
		if err != nil {
			http.Error(w, "Error writing MD5 to record file", http.StatusInternalServerError)
			return
		}

		chunkNum++
	}

	// 返回成功响应
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("File uploaded and split successfully"))
}
func DownloadFile(downloadURL string) error {
	// Step 1: 获取 links 文件
	linksResp, err := http.Get(downloadURL + "links.txt")
	if err != nil {
		return fmt.Errorf("failed to get links file: %v", err)
	}
	defer linksResp.Body.Close()

	// Step 2: 读取 links 文件的内容
	scanner := bufio.NewScanner(linksResp.Body)

	// Step 3: 第一行是文件名
	if !scanner.Scan() {
		return fmt.Errorf("failed to read filename from links file")
	}
	fileName := scanner.Text()

	// Step 4: 下面是文件的 MD5 列表
	var fileMD5s []string
	for scanner.Scan() {
		fileMD5s = append(fileMD5s, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read MD5s from links file: %v", err)
	}

	// Step 5: 下载每个文件块
	tempDir := "./temp"
	if err := os.MkdirAll(tempDir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create temp directory: %v", err)
	}

	// 逐个下载文件块
	for i, fileMD5 := range fileMD5s {
		// 下载文件块
		chunkURL := fmt.Sprintf("%s/%s.%s", downloadURL, fileMD5, "zip")
		chunkPath := filepath.Join(tempDir, fileMD5)
		if err := downloadChunk(chunkURL, chunkPath); err != nil {
			return fmt.Errorf("failed to download chunk %s: %v", fileMD5, err)
		}
		fmt.Printf("Downloaded chunk %d: %s\n", i+1, fileMD5)
	}

	// Step 6: 合并文件块
	return mergeFiles(tempDir, fileName)
}

// 下载文件块
func downloadChunk(chunkURL, chunkPath string) error {
	// 创建 GET 请求
	req, err := http.NewRequest("GET", chunkURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %v", err)
	}

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send GET request: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// 将文件块保存到本地
	file, err := os.Create(chunkPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", chunkPath, err)
	}
	defer file.Close()

	// 将文件内容写入磁盘
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to save chunk %s: %v", chunkPath, err)
	}

	return nil
}

// 合并文件块
// 合并文件块并排序
func mergeFiles(tempDir, fileName string) error {
	// 创建合并后的文件
	finalFile, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create final file: %v", err)
	}
	defer finalFile.Close()

	// 读取 temp 目录下所有文件块并合并
	files, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("failed to read temp directory: %v", err)
	}

	// 按文件名排序
	var chunkFiles []string
	for _, file := range files {
		if !file.IsDir() {
			chunkFiles = append(chunkFiles, file.Name())
		}
	}
	sort.Strings(chunkFiles) // 按文件名排序

	// 按顺序合并文件块
	for _, chunkFileName := range chunkFiles {
		chunkPath := filepath.Join(tempDir, chunkFileName)
		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			return fmt.Errorf("failed to open chunk file %s: %v", chunkPath, err)
		}

		// 将文件块写入最终文件
		_, err = io.Copy(finalFile, chunkFile)
		if err != nil {
			return fmt.Errorf("failed to merge chunk %s: %v", chunkPath, err)
		}

		chunkFile.Close()
	}

	// 删除临时文件块
	for _, file := range files {
		if !file.IsDir() {
			err := os.Remove(filepath.Join(tempDir, file.Name()))
			if err != nil {
				fmt.Printf("Failed to remove temp file %s: %v\n", file.Name(), err)
			}
		}
	}

	fmt.Printf("File %s successfully downloaded and merged.\n", fileName)
	return nil
}

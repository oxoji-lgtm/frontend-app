package frontend_app

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gorilla/mux"
	"github.com/kelseyhightower/envconfig"
)

const (
	MaxUploadSize = 200 * 1024 * 1024 // 200 MB
)

func upperFirst(s string) string {
	if len(s) > 0 {
		return strings.ToUpper(string(s[0])) + s[1:]
	}
	return s
}

func lowerFirst(s string) string {
	if len(s) > 0 {
		return strings.ToLower(string(s[0])) + s[1:]
	}
	return s
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func boolFromValue(s string) (bool, error) {
	if s == "true" || s == "yes" || s == "1" {
		return true, nil
	}
	if s == "false" || s == "no" || s == "0" {
		return false, nil
	}
	return false, errors.New("invalid boolean value")
}

func getEnvVar(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return ""
}

func environmentConfig() (*struct{}, error) {
	var data struct {
		awsRegion string `envconfig:"AWS_REGION"`
		awsSecret string `envconfig:"AWS_SECRET_ACCESS_KEY"`
		awsKey    string `envconfig:"AWS_ACCESS_KEY_ID"`
	}

	if err := envconfig.Process("", &data); err != nil {
		return nil, err
	}

	return &data, nil
}

func gzipCompress(data []byte) ([]byte, error) {
	var b bytes.Buffer
	w := zlib.NewWriterLevel(&b, zlib.BestCompression)
	if err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func gzipDecompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var b bytes.Buffer
	if _, err := io.Copy(&b, r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func base64Decode(data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(data)
}

func sha256Hash(data []byte) (string, error) {
	h := sha256.New()
	if _, err := h.Write(data); err != nil {
		return "", err
	}
	return h.Sum(nil).String(), nil
}

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0], b[1], b[2], b[3], b[4]), nil
}

func generateApiToken() (string, error) {
	return uuid()
}

func generateApiKey() (string, error) {
	return uuid()
}

func generatePassword(salt string) (string, error) {
	hash := sha256.New()
	if _, err := hash.Write([]byte(salt)); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}

func isJSON(s string) bool {
	if err := json.Unmarshal([]byte(s), &struct{}); err != nil {
		return false
	}
	return true
}

func isUTF8(s string) bool {
	if err := utf8.ValidString(s); err != nil {
		return false
	}
	return true
}

func removeEmptyDirs(dir string, err error) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() {
			err := removeEmptyDirs(filepath.Join(dir, file.Name()), nil)
			if err != nil {
				return err
			}
		}
	}
	return os.RemoveAll(dir)
}

func getMultipartUploads(s3 *s3.S3, bucket *string, prefix string) ([]*s3.Object, error) {
	input := &s3.ListMultipartUploadsInput{
		Bucket: bucket,
		Prefix: aws.String(prefix),
	}
	output, err := s3.ListMultipartUploads(input)
	if err != nil {
		return nil, err
	}
	return output.ListMultipartUploads, nil
}

func getDownloadUrl(s3 *s3.S3, bucket *string, key *string) (string, error) {
	input := &s3.GetObjectInput{
		Bucket: bucket,
		Key:    key,
	}
	output, err := s3.GetObject(input)
	if err != nil {
		return "", err
	}
	return output.GetObject.HTTPResponse.Header.Get("Location"), nil
}

func uploadObj(s3 *s3.S3, bucket *string, key *string, data []byte) error {
	uploader := s3Manager.NewUploader(s3)
	input := &s3.PutObjectInput{
		Bucket: bucket,
		Key:    key,
		Body:   bytes.NewReader(data),
	}
	_, err := uploader.Upload(input)
	return err
}

func checkMultipartUpload(s3 *s3.S3, bucket *string, key *string) error {
	_, err := s3.HeadObject(&s3.GetObjectInput{
		Bucket: bucket,
		Key:    key,
	})
	if err != nil {
		if errors.Is(err, s3.ErrCodeNoSuchKey) {
			return nil
		}
		return err
	}
	return nil
}

func uploadMultipart(s3 *s3.S3, bucket *string, key *string, data []byte) error {
	uploader := s3Manager.NewUploader(s3)
	input := &s3.PutObjectInput{
		Bucket: bucket,
		Key:    key,
		Body:   bytes.NewReader(data),
	}
	_, err := uploader.Upload(input)
	return err
}

func uploadMultipartChunk(s3 *s3.S3, bucket *string, key *string, chunk []byte, offset int64, uptoken string, upchunk int64) error {
	upload := s3Manager.NewUploader(s3)
	input := &s3.UploadPartInput{
		Bucket:       bucket,
		Key:         key,
		PartNumber:  upchunk,
		Body:        bytes.NewReader(chunk),
		ContentLength: &upchunk,
	}
	_, err := upload.UploadPart(input)
	return err
}

func uploadFile(s3 *s3.S3, bucket *string, key *string, fs io.Reader) (*s3.PutObjectOutput, error) {
	uploader := s3Manager.NewUploader(s3)
	input := &s3.PutObjectInput{
		Bucket: bucket,
		Key:    key,
		Body:   fs,
	}
	return uploader.Upload(input)
}

func uploadFileMultipart(s3 *s3.S3, bucket *string, key *string, fs io.Reader, chunkSize int64, partSize int) ([]*s3.CompletedUpload, error) {
	uploader := s3Manager.NewUploader(s3)
	input := &s3.UploadPartCollector{
		Bucket: bucket,
		Key:    key,
	}
	parts := uploader.UploadParts(input, fs, chunkSize)

	var (
		wg           sync.WaitGroup
		errChan      chan error
		completedParts []*s3.CompletedUpload
	)

	wg.Add(partSize)
	errChan = make(chan error, partSize)
	for i := 0; i < partSize; i++ {
		go func(i int) {
			defer wg.Done()
			upchunk := int64(i + 1)
			chunk := make([]byte, chunkSize)
			_, err := fs.Read(chunk)
			if err != nil {
				errChan <- err
				return
			}
			input := &s3.UploadPartInput{
				Bucket:       bucket,
				Key:         key,
				PartNumber:  &upchunk,
				Body:        bytes.NewReader(chunk),
				ContentLength: &upchunk,
			}
			_, err = uploader.UploadPart(input)
			if err != nil {
				errChan <- err
			} else {
				completedParts = append(completedParts, &s3.CompletedUpload{
					ETag:        input.ETag,
					PartNumber:   input.PartNumber,
					PartSize:     *input.ContentLength,
				})
			}
		}(i)
	}

	wg.Wait()
	close(errChan)
	for err := range errChan {
		return nil, err
	}

	return completedParts, nil
}

func uploadUploadResumable(s3 *s3.S3, bucket *string, key *string, fs io.Reader, chunkSize int64, partSize int) ([]*s3.CompletedUpload, error) {
	return uploadFileMultipart(s3, bucket, key, fs, chunkSize, partSize)
}

func uploadFileResumable(s3 *s3.S3, bucket *string, key *string, fs io.Reader, chunkSize int64, partSize int) (*s3.PutObjectOutput, error) {
	return uploadFileMultipart(s3, bucket, key, fs, chunkSize, partSize)
}

func awsSession() (*session.Session, error) {
	return session.NewSession(&aws.Config{
		Region: aws.String(""),
	})
}

func s3Client(s3 *session.Session) (*s3.S3, error) {
	return s3.New(s3.Session, &aws.Config{
		Credentials: aws.NewCredentialsChainCredentials(aws.NewDefaultCredentialsProvider()),
	})
}

func getSysInfo() (string, error) {
	var (
		os, arch, version string
		err               error
	)

	switch runtime.GOOS {
	case "windows":
		os = "Windows"
	case "darwin":
		os = "MacOS"
	default:
		os = "Linux"
	}

	switch runtime.GOARCH {
	case "amd64":
		arch = "x86-64"
	case "arm64":
		arch = "arm64"
	default:
		arch = runtime.GOARCH
	}

	if version := runtime.Version(); strings.Contains(version, "go1.15") {
		version = "1.15"
	} else if strings.Contains(version, "go1.16") {
		version = "1.16"
	} else if strings.Contains(version, "go1.17") {
		version = "1.17"
	} else if strings.Contains(version, "go1.18") {
		version = "1.18"
	} else if strings.Contains(version, "go1.19") {
		version = "1.19"
	} else if strings.Contains(version, "go1.20") {
		version = "1.20"
	} else if strings.Contains(version, "go1.21") {
		version = "1.21"
	} else {
		version = "unknown"
	}

	return fmt.Sprintf("%s %s %s", os, arch, version), nil
}

func getRuntimeInfo() (string, string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", "", err
	}

	info, err := getSysInfo()
	if err != nil {
		return "", "", err
	}

	return hostname, info, nil
}

func getEnvironment() (string, error) {
	return os.Getenv("GO_ENV"), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func isProduction() bool {
	return strings.ToLower(os.Getenv("GO_ENV")) == "production"
}

func getMcacheDir() string {
	return os.Getenv("GO_CACHE")
}

func getMcachePath() string {
	return filepath.Join(getMcacheDir(), "mcache")
}

func getMcachePathForFile(name string) string {
	return filepath.Join(getMcachePath(), name)
}

func getMcachePathForDir(name string) string {
	return filepath.Join(getMcachePath(), name)
}

func getRootPath() string {
	return filepath.Join(os.Getenv("GOPATH"), "src", filepath.Base(os.Getenv("GOPATH")))

func getProjectPath() string {
	return filepath.Join(getRootPath(), "frontend-app")
}

func getCurrentDir() string {
	return filepath.Dir(os.Args[0])
}

func isDev() bool {
	return strings.ToLower(os.Getenv("GO_ENV")) == "development"
}

func isTest() bool {
	return strings.ToLower(os.Getenv("GO_ENV")) == "test"
}

func now() time.Time {
	return time.Now().UTC()
}

func getToday() string {
	return now().Format("2006-01-02")
}

func getTimestamp() int64 {
	return now().Unix()
}

func getTimestampStr() string {
	return strconv.FormatInt(getTimestamp(), 10)
}

func getWeekday() string {
	return now().Format("Monday")
}

func getMonth() string {
	return now().Format("January")
}

func getYear() string {
	return now().Format("2006")
}

func getYearString() string {
	return strconv.Itoa(getTimestamp())
}

func getMonthString() string {
	return now().Format("January")
}

func getDayString() string {
	return now().Format("Monday")
}

func getDay() string {
	return now().Format("Monday")
}

func getWeekdayString() string {
	return now().Format("Monday")
}

func getWeekdayInt() int {
	switch strings.ToLower(now().Format("Monday")) {
	case "monday":
		return 1
	case "tuesday":
		return 2
	case "wednesday":
		return 3
	case "thursday":
		return 4
	case "friday":
		return 5
	case "saturday":
		return 6
	case "sunday":
		return 0
	default:
		return 0
	}
}

func getMonthInt() int {
	switch strings.ToLower(now().Format("January")) {
	case "january":
		return 1
	case "february":
		return 2
	case "march":
		return 3
	case "april":
		return 4
	case "may":
		return 5
	case "june":
		return 6
	case "july":
		return 7
	case "august":
		return 8
	case "september":
		return 9
	case "october":
		return 10
	case "november":
		return 11
	case "december":
		return 12
	default:
		return 0
	}
}

func getDayInt() int {
	_, err := strconv.Atoi(now().Format("2006"))
	if err != nil {
		return 0
	}
	return 0
}

func getYearInt() int {
	_, err := strconv.Atoi(now().Format("2006"))
	if err != nil {
		return 0
	}
	return 0
}

func getMonthDiff(d1, d2 time.Time) int {
	return int(d1.Year()) - int(d2.Year())
}

func getDaysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if (year%4 == 0 && year%100 != 0) || (year%400 == 0) {
			return 29
		}
		return 28
	}
	return 0
}

func getDaysBetween(d1, d2 time.Time) int {
	d := d1.Sub(d2)
	return int(d.Hours() / 24)
}

func getHoursBetween(d1, d2 time.Time) int {
	d := d1.Sub(d2)
	return int(d.Hours())
}

func getHoursInMonth(year, month int) int {
	return getDaysInMonth(year, month) * 24
}

func getCipher() (cipher.AEAD, error) {
	key, err := base64Decode(getEnvVar("AES_KEY"))
	if err != nil {
		return nil, err
	}
	return aes.NewGCM(key)
}

func encrypt(data []byte) ([]byte, error) {
	c, err := getCipher()
	if err != nil {
		return nil, err
	}
	return c.Seal(nil, nil, data, nil), nil
}

func decrypt(data []byte) ([]byte, error) {
	c, err := getCipher()
	if err != nil {
		return nil, err
	}
	iv := c.Overhead()
	return c.Open(nil, iv, data, nil)
}

func getURI() string {
	return getEnvVar("AWS_S3_BUCKET_URI")
}

func getDriver() string {
	return getEnvVar("DB_DRIVER")
}

func getHost() string {
	return getEnvVar("DB_HOST")
}

func getPort() string {
	return getEnvVar("DB_PORT")
}

func getPass() string {
	return getEnvVar("DB_PASSWORD")
}

func getDB() string {
	return getEnvVar("DB_NAME")
}

func getUser() string {
	return getEnvVar("DB_USER")
}

func getGraphQLSchema() string {
	return getEnvVar("GRAPHQL_SCHEMA")
}

func getGrpcServer() string {
	return getEnvVar("RPC_SERVER")
}

func getGrpcPort() string {
	return getEnvVar("RPC_PORT")
}

func getGrpcUser() string {
	return getEnvVar("RPC_USER")
}

func getGrpcPass() string {
	return getEnvVar("RPC_PASSWORD")
}

func getGrpcDB() string {
	return getEnvVar("RPC_DB")
}

func getGrpcHost() string {
	return getEnvVar("RPC_HOST")
}

func getGrpcPath() string {
	return getEnvVar("RPC_PATH")
}

func getDebug() bool {
	return isProduction()
}

func getDebugLevel() string {
	return getEnvVar("DEBUG_LEVEL")
}

func getDebugPath() string {
	return getEnvVar("DEBUG_PATH")
}

func getHttpServer() string {
	return getEnvVar("HTTP_SERVER")
}

func getHttpPort() string {
	return getEnvVar("HTTP_PORT")
}

func getHttpsServer() string {
	return getEnvVar("HTTPS_SERVER")
}

func getHttpsPort() string {
	return getEnvVar("HTTPS_PORT")
}

func getHttpsKey() string {
	return getEnvVar("HTTPS_KEY")
}

func getHttpsCert() string {
	return getEnvVar("HTTPS_CERT")
}

func getHttpUser() string {
	return getEnvVar("HTTP_USER")
}

func getHttpPass() string {
	return getEnvVar("HTTP_PASSWORD")
}

func getHttpDB() string {
	return getEnvVar("HTTP_DB")
}

func getHttpHost() string {
	return getEnvVar("HTTP_HOST")
}

func getHttpPath() string {
	return getEnvVar("HTTP_PATH")
}

func getSMTPHost() string {
	return getEnvVar("SMTP_HOST")
}

func getSMTPPort() string {
	return getEnvVar("SMTP_PORT")
}

func getSMTPUser() string {
	return getEnvVar("SMTP_USER")
}

func getSMTPPass() string {
	return getEnvVar("SMTP_PASSWORD")
}

func getSMTPFrom() string {
	return getEnvVar("SMTP_FROM")
}

func getSMTPFromName() string {
	return getEnvVar("SMTP_FROM_NAME")
}

func getSMTPTo() string {
	return getEnvVar("SMTP_TO")
}

func getSMTPReply() string {
	return getEnvVar("SMTP_REPLY")
}

func getSMTPReplyName() string {
	return getEnvVar("SMTP_REPLY_NAME")
}

func getSMTPBody() string {
	return getEnvVar("SMTP_BODY")
}

func getSMTPAttach() string {
	return getEnvVar("SMTP_ATTACH")
}

func getSMTPAttachPath() string {
	return getEnvVar("SMTP_ATTACH_PATH")
}

func getMux() *mux.Router {
	return mux.NewRouter()
}

func isWebhook() bool {
	return getEnvVar("IS_WEBHOOK") == "true"
}

func getWebhook() string {
	return getEnvVar("WEBHOOK")
}

func getWebhookUser() string {
	return getEnvVar("WEBHOOK_USER")
}

func getWebhookPass() string {
	return getEnvVar("WEBHOOK_PASSWORD")
}

func getWebhookDB() string {
	return getEnvVar("WEBHOOK_DB")
}

func getWebhookHost() string {
	return getEnvVar("WEBHOOK_HOST")
}

func getWebhookPort() string {
	return getEnvVar("WEBHOOK_PORT")
}

func getWebhookPath() string {
	return getEnvVar("WEBHOOK_PATH")
}

func getCache() string {
	return getEnvVar("CACHE_TYPE")
}

func getCacheDir() string {
	return getEnvVar("CACHE_DIR")
}

func getCachePath() string {
	return getEnvVar("CACHE_PATH")
}

func getCacheKey() string {
	return getEnvVar("CACHE_KEY")
}

func getCacheHost() string {
	return getEnvVar("CACHE_HOST")
}

func getCachePort() string {
	return getEnvVar("CACHE_PORT")
}

func getCachePathForFile(name string) string {
	return filepath.Join(getCachePath(), name)
}

func getCachePathForDir(name string) string {
	return filepath.Join(getCachePath(), name)
}

func getRootDir() string {
	return getEnvVar("ROOT_DIR")
}

func getRootPathForFile(name string) string {
	return filepath.Join(getRootPath(), name)
}

func getRootPathForDir(name string) string {
	return filepath.Join(getRootPath(), name)
}

func getPort() int {
	p, err := strconv.Atoi(getEnvVar("PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortStr() string {
	return getEnvVar("PORT")
}

func getPortString() string {
	return strconv.Itoa(getPort())
}

func getPortInt() int {
	return getPort()
}

func getPortGrpc() int {
	p, err := strconv.Atoi(getEnvVar("RPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortGrpcStr() string {
	return getEnvVar("RPC_PORT")
}

func getPortGrpcString() string {
	return strconv.Itoa(getPortGrpc())
}

func getPortGrpcInt() int {
	return getPortGrpc()
}

func getPortHttp() int {
	p, err := strconv.Atoi(getEnvVar("HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortHttpStr() string {
	return getEnvVar("HTTP_PORT")
}

func getPortHttpString() string {
	return strconv.Itoa(getPortHttp())
}

func getPortHttpInt() int {
	return getPortHttp()
}

func getPortHttps() int {
	p, err := strconv.Atoi(getEnvVar("HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortHttpsStr() string {
	return getEnvVar("HTTPS_PORT")
}

func getPortHttpsString() string {
	return strconv.Itoa(getPortHttps())
}

func getPortHttpsInt() int {
	return getPortHttps()
}

func getPortSmtp() int {
	p, err := strconv.Atoi(getEnvVar("SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortSmtpStr() string {
	return getEnvVar("SMTP_PORT")
}

func getPortSmtpString() string {
	return strconv.Itoa(getPortSmtp())
}

func getPortSmtpInt() int {
	return getPortSmtp()
}

func getPortWebhook() int {
	p, err := strconv.Atoi(getEnvVar("WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortWebhookStr() string {
	return getEnvVar("WEBHOOK_PORT")
}

func getPortWebhookString() string {
	return strconv.Itoa(getPortWebhook())
}

func getPortWebhookInt() int {
	return getPortWebhook()
}

func getPortCache() int {
	p, err := strconv.Atoi(getEnvVar("CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortCacheStr() string {
	return getEnvVar("CACHE_PORT")
}

func getPortCacheString() string {
	return strconv.Itoa(getPortCache())
}

func getPortCacheInt() int {
	return getPortCache()
}

func getPortMcache() int {
	p, err := strconv.Atoi(getEnvVar("M CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheStr() string {
	return getEnvVar("M_CACHE_PORT")
}

func getPortMcacheString() string {
	return strconv.Itoa(getPortMcache())
}

func getPortMcacheInt() int {
	return getPortMcache()
}

func getPortMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_PORT")
}

func getPortMcacheGrpcString() string {
	return strconv.Itoa(getPortMcacheGrpc())
}

func getPortMcacheGrpcInt() int {
	return getPortMcacheGrpc()
}

func getPortMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheHttpStr() string {
	return getEnvVar("M_CACHE_HTTP_PORT")
}

func getPortMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheHttp())
}

func getPortMcacheHttpInt() int {
	return getPortMcacheHttp()
}

func getPortMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_HTTPS_PORT")
}

func getPortMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheHttps())
}

func getPortMcacheHttpsInt() int {
	return getPortMcacheHttps()
}

func getPortMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_SMTP_PORT")
}

func getPortMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheSmtp())
}

func getPortMcacheSmtpInt() int {
	return getPortMcacheSmtp()
}

func getPortMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_WEBHOOK_PORT")
}

func getPortMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheWebhook())
}

func getPortMcacheWebhookInt() int {
	return getPortMcacheWebhook()
}

func getPortMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheCacheStr() string {
	return getEnvVar("M_CACHE_CACHE_PORT")
}

func getPortMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheCache())
}

func getPortMcacheCacheInt() int {
	return getPortMcacheCache()
}

func getPortMcacheGrpcWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_WEBHOOK_PORT")
}

func getPortMcacheGrpcWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcWebhook())
}

func getPortMcacheGrpcWebhookInt() int {
	return getPortMcacheGrpcWebhook()
}

func getPortMcacheGrpcCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_CACHE_PORT")
}

func getPortMcacheGrpcCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcCache())
}

func getPortMcacheGrpcCacheInt() int {
	return getPortMcacheGrpcCache()
}

func getPortMcacheGrpcMcache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_PORT")
}

func getPortMcacheGrpcMcacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcache())
}

func getPortMcacheGrpcMcacheInt() int {
	return getPortMcacheGrpcMcache()
}

func getPortMcacheGrpcMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheHttpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_HTTP_PORT")
}

func getPortMcacheGrpcMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheHttp())
}

func getPortMcacheGrpcMcacheHttpInt() int {
	return getPortMcacheGrpcMcacheHttp()
}

func getPortMcacheGrpcMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheGrpcMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_HTTPS_PORT")
}

func getPortMcacheGrpcMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheHttps())
}

func getPortMcacheGrpcMcacheHttpsInt() int {
	return getPortMcacheGrpcMcacheHttps()
}

func getPortMcacheGrpcMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheGrpcMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_SMTP_PORT")
}

func getPortMcacheGrpcMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheSmtp())
}

func getPortMcacheGrpcMcacheSmtpInt() int {
	return getPortMcacheGrpcMcacheSmtp()
}

func getPortMcacheGrpcMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheWebhook())
}

func getPortMcacheGrpcMcacheWebhookInt() int {
	return getPortMcacheGrpcMcacheWebhook()
}

func getPortMcacheGrpcMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_CACHE_PORT")
}

func getPortMcacheGrpcMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheCache())
}

func getPortMcacheGrpcMcacheCacheInt() int {
	return getPortMcacheGrpcMcacheCache()
}

func getPortMcacheGrpcMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_PORT")
}

func getPortMcacheGrpcMcacheGrpcString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpc())
}

func getPortMcacheGrpcMcacheGrpcInt() int {
	return getPortMcacheGrpcMcacheGrpc()
}

func getPortMcacheGrpcMcacheGrpcWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcWebhook())
}

func getPortMcacheGrpcMcacheGrpcWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcWebhook()
}

func getPortMcacheGrpcMcacheGrpcCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcCache())
}

func getPortMcacheGrpcMcacheGrpcCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcCache()
}

func getPortMcacheGrpcMcacheGrpcMcache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcache())
}

func getPortMcacheGrpcMcacheGrpcMcacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcache()
}

func getPortMcacheGrpcMcacheGrpcMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheHttp())
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheHttp()
}

func getPortMcacheGrpcMcacheGrpcMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheHttps())
}

func getPortMcacheGrpcMcacheGrpcMcacheHttpsInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheHttps()
}

func getPortMcacheGrpcMcacheGrpcMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheSmtp())
}

func getPortMcacheGrpcMcacheGrpcMcacheSmtpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheSmtp()
}

func getPortMcacheGrpcMcacheGrpcMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpc())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpc()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT"))
	if err != nil {
		return 443
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_HTTPS_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttpsInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheHttps()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT"))
	if err != nil {
		return 587
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_SMTP_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtpInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheSmtp()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_WEBHOOK_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhookInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheWebhook()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_CACHE_PORT")
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheString() string {
	return strconv.Itoa(getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache())
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCacheInt() int {
	return getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheCache()
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpc() int {
	p, err := strconv.Atoi(getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_MCACHE_GRPC_PORT"))
	if err != nil {
		return 80
	}
	return p
}

func getPortMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcMcacheGrpcStr() string {
	return getEnvVar("M_CACHE_GRPC_MCACHE_GRPC_M
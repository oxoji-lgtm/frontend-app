package frontend_app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func GenerateUUID() string {
	uuid := make([]byte, 16)
	rand.Read(uuid)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}

func Sha256Hash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func GetHTTPStatusCode(status string) (int, error) {
	switch status {
	case "OK":
		return http.StatusOK, nil
	case "Created":
		return http.StatusCreated, nil
	case "No Content":
		return http.StatusNoContent, nil
	case "Not Found":
		return http.StatusNotFound, nil
	case "Unauthorized":
		return http.StatusUnauthorized, nil
	case "Forbidden":
		return http.StatusForbidden, nil
	case "InternalServerError":
		return http.StatusInternalServerError, nil
	case "Bad Request":
		return http.StatusBadRequest, nil
	default:
		return http.StatusInternalServerError, fmt.Errorf("unknown status: %s", status)
	}
}

func GetYearMonthDay() string {
	return time.Now().Format("2006-01-02")
}

func GetYearMonthDayHourMinute() string {
	return time.Now().Format("2006-01-02 15:04")
}

func GetFileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err!= nil {
		return 0, err
	}
	return info.Size(), nil
}

func GetFileExtension(filePath string) string {
	return filepath.Ext(filePath)
}

func IsEmptyString(s string) bool {
	return s == ""
}

func IsNil(p interface{}) bool {
	if p == nil {
		return true
	}
	return false
}

func GetMaxUint8() uint8 {
	return 255
}

func GetMaxInt8() int8 {
	return 127
}

func GetMaxUint16() uint16 {
	return 65535
}

func GetMaxInt16() int16 {
	return 32767
}

func GetMaxUint32() uint32 {
	return 4294967295
}

func GetMaxInt32() int32 {
	return 2147483647
}

func GetMaxUint64() uint64 {
	return 18446744073709551615
}

func GetMaxInt64() int64 {
	return 9223372036854775807
}

func StringToInt(s string) (int, error) {
	i, err := strconv.Atoi(s)
	if err!= nil {
		return 0, err
	}
	return i, nil
}

func StringsToIntSlice(s string) ([]int, error) {
	ss := strings.Split(s, ",")
	ints := make([]int, 0, len(ss))
	for _, s := range ss {
		i, err := strconv.Atoi(s)
		if err!= nil {
			return nil, err
		}
		ints = append(ints, i)
	}
	return ints, nil
}

func IsPortAvailable(port int) bool {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Port is in use", http.StatusConflict)
	})
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port)}
	go func() {
		log.Fatal(srv.ListenAndServe())
	}()
	select {
	case <-time.After(1 * time.Second):
		return true
	case <-srv.Err():
		return false
	}
}

func GetLogLevel(level string) int {
	switch level {
	case "debug":
		return 0
	case "info":
		return 1
	case "warning":
		return 2
	case "error":
		return 3
	case "fatal":
		return 4
	default:
		return 4
	}
}
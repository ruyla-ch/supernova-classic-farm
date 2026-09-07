package game

// 本文件负责账号格式检查、密码派生和安全随机编号生成。

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const passwordIterations = 600000

var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

func ValidateCredentials(username, password string) error {
	if !usernamePattern.MatchString(username) || !utf8.ValidString(password) || len(password) < 8 || len(password) > 128 {
		return ErrInvalid
	}
	return nil
}
func randomHex(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func HashPassword(password string) (string, error) {
	// 每个密码使用独立随机盐；数据库只保存算法参数、盐和派生结果。
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$600000$%x$%x", salt, key), nil
}
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return false
	}
	salt, e1 := hex.DecodeString(parts[2])
	want, e2 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	// 常量时间比较降低根据响应时间猜测密码哈希的风险。
	return err == nil && subtle.ConstantTimeCompare(key, want) == 1
}

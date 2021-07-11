package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	_ "github.com/joho/godotenv/autoload"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/speps/go-hashids/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	hd          *hashids.HashIDData
	db          *gorm.DB
	minioClient *minio.Client
	bucketName  string
	count       int64
)

type Link struct {
	ID          uint            `gorm:"primarykey" json:"-"`
	Source      string          `json:"source"`
	Code        string          `gorm:"index" json:"code"`
	Key         string          `gorm:"key" json:"key"`
	ContentType string          `gorm:"content_type" json:"content_type"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	DeletedAt   *gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

func init() {
	hd = hashids.NewData()
	hd.Salt = os.Getenv("APP_KEY")
	hd.MinLength = 5

	dsn := fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true",
		os.Getenv("DB_USERNAME"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_DATABASE"),
	)
	var err error
	if db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{}); err != nil {
		log.Fatal(err.Error())
	}
	if err = db.AutoMigrate(&Link{}); err != nil {
		log.Fatal(err.Error())
	}

	endpoint := os.Getenv("MINIO_ENDPOINT")
	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	bucketName = os.Getenv("MINIO_BUCKET")
	if minioClient, err = minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
	}); err != nil {
		log.Fatal(err)
	}
	exists, err := minioClient.BucketExists(context.Background(), bucketName)
	if err != nil {
		log.Fatal(err.Error())
	}
	if !exists {
		opts := minio.MakeBucketOptions{}
		if err = minioClient.MakeBucket(context.Background(), bucketName, opts); err != nil {
			log.Fatal(err.Error())
		}
	}
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/{code}", Redirect).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/api/links/{code}", ShowLink).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/api/links", StoreLink).Methods(http.MethodPost, http.MethodOptions)
	r.HandleFunc("/api/links/{code}", DestroyLink).Methods(http.MethodDelete, http.MethodOptions)
	r.HandleFunc("/api/objects/{object}", ShowObject).Methods(http.MethodGet, http.MethodOptions)
	r.HandleFunc("/api/objects", StoreObject).Methods(http.MethodPost, http.MethodOptions)
	log.Fatal(http.ListenAndServe(":80", r))
}

func Redirect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	link := Link{}
	if err := find(mux.Vars(r)["code"], &link); err != nil {
		response(w, http.StatusNotFound, nil)
		return
	}
	http.Redirect(w, r, link.Source, http.StatusMovedPermanently)
}

func StoreLink(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	link := Link{}
	if err := json.NewDecoder(r.Body).Decode(&link); err != nil {
		response(w, http.StatusInternalServerError, Payload{Error: err.Error()})
		return
	}
	store(&link)
	response(w, http.StatusCreated, Payload{
		Data: link,
	})
}

func ShowLink(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	link := Link{}
	if err := find(mux.Vars(r)["code"], &link); err != nil {
		response(w, http.StatusNotFound, nil)
		return
	}
	response(w, http.StatusOK, Payload{
		Data: link,
	})
}

func DestroyLink(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	link := Link{}
	if err := find(mux.Vars(r)["code"], &link); err != nil {
		response(w, http.StatusNotFound, nil)
		return
	}
	if link.Key != "" && link.Key != r.URL.Query().Get("key") {
		response(w, http.StatusForbidden, nil)
		return
	}
	if link.ContentType != "" {
		objectName := filepath.Base(link.Source)
		opts := minio.RemoveObjectOptions{}
		err := minioClient.RemoveObject(context.Background(), bucketName, objectName, opts)
		if err != nil {
			response(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	db.Delete(&link)
	response(w, http.StatusNoContent, nil)
}

func StoreObject(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		response(w, http.StatusBadRequest, err.Error())
		return
	}
	fileHeaders := r.MultipartForm.File["files[]"]
	key := r.MultipartForm.Value["key"][0]
	if len(fileHeaders) < 1 {
		response(w, http.StatusBadRequest, nil)
		return
	}
	header := fileHeaders[0]
	file, err := header.Open()
	if err != nil {
		response(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Println(err.Error())
		}
	}()
	objectName := strings.ReplaceAll(uuid.NewString(), "-", "") + filepath.Ext(header.Filename)
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		response(w, http.StatusInternalServerError, err.Error())
		return
	}
	contentType := http.DetectContentType(b)
	opts := minio.PutObjectOptions{ContentType: contentType}
	_, err = minioClient.PutObject(context.Background(), bucketName, objectName, file, header.Size, opts)
	if err != nil {
		response(w, http.StatusInternalServerError, err.Error())
		return
	}
	link := Link{
		Source:      fmt.Sprintf("%s/%s", os.Getenv("APP_OBJECT_ENDPOINT"), objectName),
		Key:         key,
		ContentType: contentType,
	}
	store(&link)
	response(w, http.StatusCreated, Payload{
		Data: link,
	})
}

func ShowObject(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		response(w, http.StatusOK, nil)
		return
	}
	objectName := mux.Vars(r)["object"]
	opts := minio.GetObjectOptions{}
	object, err := minioClient.GetObject(context.Background(), bucketName, objectName, opts)
	if err != nil {
		response(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if _, err = io.Copy(w, object); err != nil {
		response(w, http.StatusNotFound, err.Error())
	}
}

func find(code string, link *Link) error {
	id, err := decode(code)
	if err != nil {
		return err
	}
	err = db.Where("id = ?", id).First(link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func store(link *Link) {
	if count == 0 {
		db.Model(link).Unscoped().Count(&count)
	}
	count++
	link.Code = encode(count)
	db.Create(link)
}

func encode(id int64) string {
	h, _ := hashids.NewWithData(hd)
	e, _ := h.Encode([]int{int(id)})
	return e
}

func decode(code string) (int, error) {
	h, _ := hashids.NewWithData(hd)
	d, err := h.DecodeWithError(code)
	if err != nil {
		return 0, err
	}
	return d[0], nil
}

type Payload struct {
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func response(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

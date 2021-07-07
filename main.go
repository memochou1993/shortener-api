package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/mux"
	_ "github.com/joho/godotenv/autoload"
	"github.com/speps/go-hashids/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	db    *gorm.DB
	count int64
)

type Link struct {
	ID        uint            `gorm:"primarykey" json:"id"`
	Source    string          `json:"source"`
	Code      string          `gorm:"index" json:"code"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

func init() {
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
}

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/api/links/{code}", ShowLink).Methods(http.MethodGet)
	r.HandleFunc("/api/links", StoreLink).Methods(http.MethodPost)
	r.HandleFunc("/api/links/{code}", DestroyLink).Methods(http.MethodDelete)

	log.Fatal(http.ListenAndServe(":80", r))
}

func StoreLink(w http.ResponseWriter, r *http.Request) {
	link := Link{}
	if err := json.NewDecoder(r.Body).Decode(&link); err != nil {
		response(w, http.StatusInternalServerError, Payload{Error: err.Error()})
		return
	}

	if count == 0 {
		db.Model(&Link{}).Unscoped().Count(&count)
	}
	count++
	link.Code = encode(count)

	db.Create(&link)

	response(w, http.StatusCreated, Payload{
		Data: link,
	})
}

func ShowLink(w http.ResponseWriter, r *http.Request) {
	code := mux.Vars(r)["code"]

	link := Link{}
	err := db.Where("code = ?", code).First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response(w, http.StatusNotFound, nil)
		return
	}

	response(w, http.StatusOK, Payload{
		Data: link,
	})
}

func DestroyLink(w http.ResponseWriter, r *http.Request) {
	code := mux.Vars(r)["code"]

	link := Link{}
	err := db.Where("code = ?", code).First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response(w, http.StatusNotFound, nil)
		return
	}

	db.Delete(&link)

	response(w, http.StatusNoContent, nil)
}

type Payload struct {
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func response(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func encode(id int64) string {
	hd := hashids.NewData()
	hd.Salt = os.Getenv("APP_KEY")
	hd.MinLength = 5

	h, _ := hashids.NewWithData(hd)
	e, _ := h.Encode([]int{int(id)})
	return e
}

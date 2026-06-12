package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/nedpals/supabase-go"
)

var supabaseClient *supabase.Client

func init() {
	godotenv.Load() // Loads .env file
	supabaseURL := "https://whjtoxddmmlzmhsjlrzi.supabase.co"
	
	supabaseKey := os.Getenv("SUPABASE_KEY")
	
	supabaseClient = supabase.CreateClient(supabaseURL, supabaseKey)
}

func main() {
	router := gin.Default()

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "Pong!"})
	})

	api := router.Group("/api")
	{
		api.POST("/register", RegisterFace)
		api.POST("/login", LoginFace)
	}

	router.Run(":8000") 
}

func RegisterFace(c *gin.Context) {
	namaLengkap := c.PostForm("nama_lengkap")
	if namaLengkap == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Nama lengkap wajib diisi"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Gagal menerima file foto"})
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal membuka file"})
		return
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", fileHeader.Filename)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal membuat paket data"})
		return
	}
	io.Copy(part, file)
	writer.Close()

	pythonURL := "http://localhost:5000/extract-encoding"
	req, err := http.NewRequest("POST", pythonURL, &requestBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal membuat request ke Python"})
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Python AI Worker tidak merespons. Pastikan port 5000 menyala."})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// kalo python gagal detect wajah
	if resp.StatusCode != http.StatusOK {
		var errorResponse map[string]interface{}
		json.Unmarshal(respBody, &errorResponse)
		c.JSON(resp.StatusCode, errorResponse)
		return
	}

	// nyimpen data ke db
	var pythonResponse map[string]interface{}
	json.Unmarshal(respBody, &pythonResponse)

	faceEncoding := pythonResponse["encoding"]

	dataUser := map[string]interface{}{
		"nama_lengkap":  namaLengkap,
		"face_encoding": faceEncoding,
	}

	var results []interface{}
	err = supabaseClient.DB.From("users").Insert(dataUser).Execute(&results)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal menyimpan ke database: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Registrasi wajah berhasil dan tersimpan di database!",
	})
}

func LoginFace(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Gagal menerima file foto"})
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal membuka file"})
		return
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	part, _ := writer.CreateFormFile("file", fileHeader.Filename)
	io.Copy(part, file)
	writer.Close()

	pythonURL := "http://localhost:5000/extract-encoding"
	req, _ := http.NewRequest("POST", pythonURL, &requestBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengekstrak wajah atau wajah tidak terdeteksi"})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var pythonResponse map[string]interface{}
	json.Unmarshal(respBody, &pythonResponse)

	loginEncodingInterface := pythonResponse["encoding"].([]interface{})
	var loginEncoding []float64
	for _, v := range loginEncodingInterface {
		loginEncoding = append(loginEncoding, v.(float64))
	}

	var users []map[string]interface{}
	err = supabaseClient.DB.From("users").Select("nama_lengkap,face_encoding").Execute(&users)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gagal mengambil data dari database:" + err.Error() })
		return
	}

	batasToleransi := 0.6 
	wajahDitemukan := false
	namaPengguna := ""
	jarakTerdekat := 1.0 

	for _, user := range users {
		dbEncodingInterface := user["face_encoding"].([]interface{})
		var dbEncoding []float64
		for _, v := range dbEncodingInterface {
			dbEncoding = append(dbEncoding, v.(float64))
		}

		jarak := hitungJarakWajah(loginEncoding, dbEncoding)

		if jarak <= batasToleransi && jarak < jarakTerdekat {
			jarakTerdekat = jarak
			wajahDitemukan = true
			namaPengguna = user["nama_lengkap"].(string)
		}
	}

	if wajahDitemukan {
		c.JSON(http.StatusOK, gin.H{
			"status":   "sukses",
			"message":  "Login berhasil!",
			"nama":     namaPengguna,
			"distance": jarakTerdekat,
		})
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{
			"status":  "gagal",
			"error":   "Wajah tidak dikenali dalam sistem",
		})
	}
}

func hitungJarakWajah(encoding1 []float64, encoding2 []float64) float64 {
	var sum float64
	for i := 0; i < len(encoding1); i++ {
		diff := encoding1[i] - encoding2[i]
		sum += diff * diff
	}
	return math.Sqrt(sum)
}
package main

import (
    "fmt"
    "log"
    "net/http"
)

func home(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintf(w, "Api funcionando!")
}


func main() {
    http.HandleFunc("/", home)

    fmt.Println("Iniciando o servidor na porta 8080...")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

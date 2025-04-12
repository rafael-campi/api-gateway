package main

import (
    "fmt"
    "log"
    "net/http"
)

// Função de handler para a rota "/"
func home(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintf(w, "Api funcionando!")
}


func main() {
    // Definindo as rotas
    http.HandleFunc("/", home)

    // Inicializando o servidor na porta 8080
    fmt.Println("Iniciando o servidor na porta 8080...")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

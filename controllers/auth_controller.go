package controllers

import (
	"context"
	"net/http"

	"raahi-backend/config"
	"raahi-backend/models"
	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/crypto/bcrypt"
)

var userCollection *mongo.Collection

func InitializeAuthCollection() {
	userCollection = config.Database.Collection("users")
}

func Register(c *gin.Context) {
	var body models.User
	c.BindJSON(&body)

	hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), 10)
	body.Password = string(hash)

	userCollection.InsertOne(context.Background(), body)
	c.JSON(http.StatusCreated, gin.H{"message": "User registered"})
}

func Login(c *gin.Context) {
	var body models.User
	c.BindJSON(&body)

	var user models.User
	err := userCollection.FindOne(
		context.Background(),
		bson.M{"email": body.Email},
	).Decode(&user)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(body.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	token, _ := utils.GenerateJWT(user.ID)
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func DeleteUser(c *gin.Context) {
	id := c.Param("id")
	res, err := userCollection.DeleteOne(context.Background(), bson.M{"_id": id})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}
	if res.DeletedCount == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

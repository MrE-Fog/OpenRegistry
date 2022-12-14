package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/containerish/OpenRegistry/config"
	"github.com/containerish/OpenRegistry/services/email"
	"github.com/containerish/OpenRegistry/store/postgres"
	"github.com/containerish/OpenRegistry/types"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func (a *auth) SignUp(ctx echo.Context) error {
	ctx.Set(types.HandlerStartTime, time.Now())

	var u types.User
	if err := json.NewDecoder(ctx.Request().Body).Decode(&u); err != nil {
		echoErr := ctx.JSON(http.StatusBadRequest, echo.Map{
			"error":   err.Error(),
			"message": "error decoding request body in sign-up",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}
	_ = ctx.Request().Body.Close()

	if err := u.Validate(); err != nil {
		echoErr := ctx.JSON(http.StatusBadRequest, echo.Map{
			"error":   err.Error(),
			"message": "invalid request for user sign up",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}

	passwordHash, err := a.hashPassword(u.Password)
	if err != nil {
		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "internal server error: could not hash the password",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}

	u.Password = passwordHash
	id, err := uuid.NewRandom()
	if err != nil {
		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "error creating random id for user sign-up",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}
	newUser := &types.User{
		Email:    u.Email,
		Username: u.Username,
		Password: u.Password,
		Id:       id.String(),
	}

	newUser.Hireable = false
	newUser.HTMLURL = ""

	// no need to do email verification in local mode
	isEnvironemtElevated := a.c.Environment == config.Staging || a.c.Environment == config.Production
	if !isEnvironemtElevated {
		newUser.IsActive = true
	}

	err = a.pgStore.AddUser(ctx.Request().Context(), newUser)
	if err != nil {
		if strings.Contains(err.Error(), postgres.ErrDuplicateConstraintUsername) {
			echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
				"error":   err.Error(),
				"message": "username already exists",
			})
			a.logger.Log(ctx, err)
			return echoErr
		}

		if strings.Contains(err.Error(), postgres.ErrDuplicateConstraintEmail) {
			echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
				"error":   err.Error(),
				"message": "this email already taken, try sign in?",
			})
			a.logger.Log(ctx, err)
			return echoErr
		}

		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "could not persist the user",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}

	// in case of CI setup, no need to send verification emails
	if !isEnvironemtElevated {
		msg := fmt.Errorf("user successfully created")
		echoErr := ctx.JSON(http.StatusCreated, echo.Map{
			"message": msg,
		})
		a.logger.Log(ctx, msg)
		return echoErr
	}

	token, err := uuid.NewRandom()
	if err != nil {
		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "error creating random id for token",
		})
		a.logger.Log(ctx, err)
		return echoErr

	}
	err = a.pgStore.AddVerifyEmail(ctx.Request().Context(), token.String(), newUser.Id)
	if err != nil {
		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "error adding verify email",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}

	err = a.emailClient.SendEmail(newUser, token.String(), email.VerifyEmailKind)
	if err != nil {
		echoErr := ctx.JSON(http.StatusInternalServerError, echo.Map{
			"error":   err.Error(),
			"message": "could not send verify link, please reach out to OpenRegistry Team",
		})
		a.logger.Log(ctx, err)
		return echoErr
	}

	echoErr := ctx.JSON(http.StatusCreated, echo.Map{
		"message": "signup was successful, please check your email to activate your account",
	})
	a.logger.Log(ctx, echoErr)
	return echoErr
}

// nolint
func verifyEmail(email string) error {
	if email == "" {
		return fmt.Errorf("email can not be empty")
	}
	emailReg := regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+\\/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}" +
		"[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

	if !emailReg.Match([]byte(email)) {
		return fmt.Errorf("email format invalid")
	}

	return nil
}

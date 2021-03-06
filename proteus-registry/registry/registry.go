package registry

import (
	"fmt"
	"time"
	"errors"
	"net/http"
	"database/sql"
	
	"github.com/thetorproject/proteus/proteus-common/middleware"

	"github.com/jmoiron/sqlx"
	"github.com/apex/log"
	"github.com/satori/go.uuid"
	"github.com/rubenv/sql-migrate"
	"github.com/lib/pq"
	"github.com/spf13/viper"
	"github.com/gin-gonic/gin"
	"github.com/facebookgo/grace/gracehttp"
	"gopkg.in/gin-contrib/cors.v1"
	"golang.org/x/crypto/bcrypt"
)

var ctx = log.WithFields(log.Fields{
	"cmd": "registry",
})

func initDatabase() (*sqlx.DB, error) {
	db, err := sqlx.Open("postgres", viper.GetString("database.url"))
	if err != nil {
		ctx.Error("failed to open database")
		return nil, err
	}
	return db, err
}

type ClientData struct {
	ProbeCC string `json:"probe_cc" binding:"required"`
	ProbeASN string `json:"probe_asn" binding:"required"`
	Platform string `json:"platform" binding:"required"`

	SoftwareName string `json:"software_name" binding:"required"`
	SoftwareVersion string `json:"software_version" binding:"required"`
	SupportedTests []string `json:"supported_tests" binding:"required"`

	NetworkType string `json:"network_type"`
	AvailableBandwidth string `json:"available_bandwidth"`
	Language string `json:"language"`

	Token string `json:"token"`

	ProbeFamily string `json:"probe_family"`
	ProbeID string `json:"probe_id"`

	Password string `json:"password"`
}

func IsClientRegistered(db *sqlx.DB, clientID string) (bool, error) {
	var found string
	query := fmt.Sprintf(`SELECT id FROM %s WHERE id = $1`,
				pq.QuoteIdentifier(viper.GetString("database.active-probes-table")))
	err := db.QueryRow(query, clientID).Scan(&found)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func Update(db *sqlx.DB, clientID string, req ClientData) (error) {
	tx, err := db.Begin()
	if err != nil {
		ctx.WithError(err).Error("failed to open transaction")
		return err
	}

	// Write into the updates table
	{
		query := fmt.Sprintf(`INSERT INTO %s (
			id, update_time,
			client_id,
			probe_cc, probe_asn,
			platform, software_name,
			software_version, supported_tests,
			network_type, available_bandwidth,
			lang_code,
			token, probe_family,
			probe_id, update_type
		) VALUES (
			$1, $2,
			$3,
			$4, $5,
			$6, $7,
			$8, $9,
			$10, $11,
			$12,
			$13, $14,
			$15, $16)`,
			pq.QuoteIdentifier(viper.GetString("database.probe-updates-table")))

		stmt, err := tx.Prepare(query)
		if (err != nil) {
			ctx.WithError(err).Error("failed to prepare update probes query")
			return err
		}

		updateID := uuid.NewV4().String()
		_, err = stmt.Exec(updateID, time.Now().UTC(),
							clientID,
							req.ProbeCC, req.ProbeASN,
							req.Platform, req.SoftwareName,
							req.SoftwareVersion, pq.Array(req.SupportedTests),
							req.NetworkType, req.AvailableBandwidth,
							req.Language,
							req.Token, req.ProbeFamily,
							req.ProbeID, "register")
		if (err != nil) {
			ctx.WithError(err).Error("failed to add data to update table, rolling back")
			tx.Rollback()
			return errors.New("error in adding data to update probes")
		}
	}

	// Write into the active probes table
	{
		query := fmt.Sprintf(`UPDATE %s SET
			last_updated = $2,
			probe_cc = $3,
			probe_asn = $4,
			platform = $5,
			software_name = $6,
			software_version = $7,
			supported_tests = $8,
			network_type = $9,
			available_bandwidth = $10,
			lang_code = $11,
			token = $12,
			probe_family = $13,
			probe_id = $14
			WHERE id = $1`,
			pq.QuoteIdentifier(viper.GetString("database.active-probes-table")))

		stmt, err := tx.Prepare(query)
		if (err != nil) {
			ctx.WithError(err).Error("failed to prepare update probes query")
			return err
		}
		_, err = stmt.Exec(clientID,
							time.Now().UTC(),
							req.ProbeCC,
							req.ProbeASN,
							req.Platform,
							req.SoftwareName,
							req.SoftwareVersion,
							pq.Array(req.SupportedTests),
							req.NetworkType,
							req.AvailableBandwidth,
							req.Language,
							req.Token,
							req.ProbeFamily,
							req.ProbeID)
		if (err != nil) {
			ctx.WithError(err).Error("failed to update active table, rolling back")
			tx.Rollback()
			return errors.New("failed to update active table")
		}
	}

	if err := tx.Commit(); err != nil {
		ctx.WithError(err).Error("failed to commit transaction, rolling back")
		return err
	}

	return nil
}


func Register(db *sqlx.DB, req ClientData) (string, error) {
	if ((req.Platform == "ios" || req.Platform == "android") && req.Token == "") {
		return "", errors.New("missing device token")
	}
	if (req.Password == "") {
		return "", errors.New("missing password")
	}

	tx, err := db.Begin()
	if err != nil {
		ctx.WithError(err).Error("failed to open transaction")
		return "", err
	}

	var clientID = uuid.NewV4().String()

	{
		query := fmt.Sprintf(`INSERT INTO %s (
			id, creation_time,
			last_updated,
			probe_cc, probe_asn,
			platform, software_name,
			software_version, supported_tests,
			network_type, available_bandwidth,
			lang_code,
			token, probe_family,
			probe_id
		) VALUES (
			$1, $2,
			$3,
			$4, $5,
			$6, $7,
			$8, $9,
			$10, $11,
			$12,
			$13, $14,
			$15)`,
			pq.QuoteIdentifier(viper.GetString("database.active-probes-table")))

		stmt, err := tx.Prepare(query)
		if (err != nil) {
			ctx.WithError(err).Error("failed to prepare active probes query")
			return "", err
		}
		defer stmt.Close()

		_, err = stmt.Exec(clientID, time.Now().UTC(),
							time.Now().UTC(),
							req.ProbeCC, req.ProbeASN,
							req.Platform, req.SoftwareName,
							req.SoftwareVersion, pq.Array(req.SupportedTests),
							req.NetworkType, req.AvailableBandwidth,
							req.Language,
							req.Token, req.ProbeFamily,
							req.ProbeID)
		if err != nil {
			tx.Rollback()
			ctx.WithError(err).Error("failed to insert into active probes table, rolling back")
			return "", err
		}
	}

	{
		query := fmt.Sprintf(`INSERT INTO %s (
			id, update_time,
			client_id,
			probe_cc, probe_asn,
			platform, software_name,
			software_version, supported_tests,
			network_type, available_bandwidth,
			lang_code,
			token, probe_family,
			probe_id, update_type
		) VALUES (
			$1, $2,
			$3,
			$4, $5,
			$6, $7,
			$8, $9,
			$10, $11,
			$12,
			$13, $14,
			$15, $16)`,
			pq.QuoteIdentifier(viper.GetString("database.probe-updates-table")))

		stmt, err := tx.Prepare(query)
		if (err != nil) {
			ctx.WithError(err).Error("failed to prepare update probes query")
			return "", err
		}
		defer stmt.Close()

		updateID := uuid.NewV4().String()
		_, err = stmt.Exec(updateID, time.Now().UTC(),
							clientID,
							req.ProbeCC, req.ProbeASN,
							req.Platform, req.SoftwareName,
							req.SoftwareVersion, pq.Array(req.SupportedTests),
							req.NetworkType, req.AvailableBandwidth,
							req.Language,
							req.Token, req.ProbeFamily,
							req.ProbeID, "register")
		if (err != nil) {
			ctx.WithError(err).Error("failed to add data to update table, rolling back")
			tx.Rollback()
			return "", errors.New("error in adding data to update probes")
		}
	}

	{
		query := fmt.Sprintf(`INSERT INTO %s (
			username,
			password_hash,
			last_access,
			role
		) VALUES (
			$1,
			$2,
			$3,
			$4)`,
			pq.QuoteIdentifier(viper.GetString("database.accounts-table")))

		passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			ctx.WithError(err).Error("failed to hash password")
			return "", err
		}

		stmt, err := tx.Prepare(query)
		if (err != nil) {
			ctx.WithError(err).Error("failed to prepare accounts query")
			return "", err
		}
		defer stmt.Close()

		_, err = stmt.Exec(clientID,
							string(passwordHash),
							time.Now().UTC(),
							"device")
		if err != nil {
			tx.Rollback()
			ctx.WithError(err).Error("failed to insert into accounts table, rolling back")
			return "", err
		}
	}

	if err := tx.Commit(); err != nil {
		ctx.WithError(err).Error("failed to commit transaction, rolling back")
		return "", err
	}

	return clientID, nil
}

type ActiveClient struct {
	ClientID			string `json:"client_id"`

	ProbeCC				string `json:"probe_cc"`
	ProbeASN			string `json:"probe_asn"`
	Platform			string `json:"platform"`

	SoftwareName		string `json:"software_name"`
	SoftwareVersion		string `json:"software_version"`
	SupportedTests		string `json:"supported_tests"`

	NetworkType			string `json:"network_type"`
	AvailableBandwidth	string `json:"available_bandwidth"`
	Language			string `json:"language"`
	
	Token				string `json:"token"`

	ProbeFamily			string `json:"probe_family"`
	ProbeID				string `json:"probe_id"`

	LastUpdated			time.Time `json:"last_updated"`
	CreationTime		time.Time `json:"creation_time"`
}


func ListClients(db *sqlx.DB) ([]ActiveClient, error) {
	var activeClients []ActiveClient
	query := fmt.Sprintf(`SELECT
			id, creation_time,
			last_updated,
			probe_cc, probe_asn,
			platform, software_name,
			software_version, supported_tests,
			network_type, available_bandwidth,
			lang_code,
			token, probe_family,
			probe_id FROM %s`,
		pq.QuoteIdentifier(viper.GetString("database.active-probes-table")))

	rows, err := db.Query(query)
	if err != nil {
		ctx.WithError(err).Error("failed to list clients")
		return activeClients, err
	}
	defer rows.Close()
	for rows.Next() {
		var ac ActiveClient
		err := rows.Scan(&ac.ClientID,
						&ac.CreationTime,
						&ac.LastUpdated,
						&ac.ProbeCC,
						&ac.ProbeASN,
						&ac.Platform,
						&ac.SoftwareName,
						&ac.SoftwareVersion,
						&ac.SupportedTests,
						&ac.NetworkType,
						&ac.AvailableBandwidth,
						&ac.Language,
						&ac.Token,
						&ac.ProbeFamily,
						&ac.ProbeID)
		if err != nil {
			ctx.WithError(err).Error("failed to iterate over clients")
			return activeClients, err
		}
		activeClients = append(activeClients, ac)
	}
	return activeClients, nil
}

func runMigrations(db *sqlx.DB) (error) {
	migrations := &migrate.AssetMigrationSource{
		Asset: Asset,
		AssetDir: AssetDir,
		Dir: "data/migrations",
	}
	n, err := migrate.Exec(db.DB, "postgres", migrations, migrate.Up)
	if err != nil {
		return err
	}
	ctx.Infof("performed %d migrations", n)
	return nil
}

func Start() {
	db, err := initDatabase()

	if (err != nil) {
		ctx.WithError(err).Error("failed to connect to DB")
		return
	}
	defer db.Close()
	err = runMigrations(db)
	if (err != nil) {
		ctx.WithError(err).Error("failed to run DB migration")
		return
	}

	authMiddleware, err := proteus_mw.InitAuthMiddleware(db)
	if (err != nil) {
		ctx.WithError(err).Error("failed to initialise authMiddlewareDevice")
		return
	}

	router := gin.Default()
	router.Use(cors.New(proteus_mw.CorsConfig()))

	v1 := router.Group("/api/v1")
	v1.POST("/login", authMiddleware.LoginHandler)
	v1.POST("/register", func(c *gin.Context) {
		var registerReq ClientData
		err := c.BindJSON(&registerReq)
		if (err != nil) {
			ctx.WithError(err).Error("invalid request")
			c.JSON(http.StatusBadRequest,
					gin.H{"error": "invalid request"})
			return
		}

		clientID, err := Register(db, registerReq)
		if (err != nil) {
			c.JSON(http.StatusBadRequest,
					gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"client_id": clientID})
		return
	})

	admin := v1.Group("/admin")
	admin.Use(authMiddleware.MiddlewareFunc(proteus_mw.AdminAuthorizor))
	{
		admin.GET("/clients", func(c *gin.Context) {
			clientList, err := ListClients(db)
			if err != nil {
				c.JSON(http.StatusBadRequest,
						gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK,
					gin.H{"active_clients": clientList})
		})
	}

	device := v1.Group("/")
	device.Use(authMiddleware.MiddlewareFunc(proteus_mw.DeviceAuthorizor))
	{
		// XXX do we also want to support a PATCH method?
		device.PUT("/update/:client_id", func(c *gin.Context) {
			var updateReq ClientData
			clientID := c.Param("client_id")
			err := c.BindJSON(&updateReq)
			if (err != nil) {
				ctx.WithError(err).Error("invalid request")
				c.JSON(http.StatusBadRequest,
						gin.H{"error": "invalid request"})
				return
			}
			isRegistered, err := IsClientRegistered(db, clientID)
			if (err != nil) {
				ctx.WithError(err).Error("failed to learn if client is registered")
				c.JSON(http.StatusBadRequest,
						gin.H{"error": err.Error()})
				return
			}
			if (isRegistered == false) {
				c.JSON(http.StatusNotFound,
						gin.H{"error": "client is not registered"})
				return
			}

			err = Update(db, clientID, updateReq)
			if (err != nil) {
				ctx.WithError(err).Error("failed to update")
				c.JSON(http.StatusBadRequest,
						gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK,
					gin.H{"status": "ok"})
		})
	}

	Addr := fmt.Sprintf("%s:%d", viper.GetString("api.address"),
								viper.GetInt("api.port"))
	ctx.Infof("starting on %s", Addr)
	s := &http.Server{
		Addr: Addr,
		Handler: router,
	}
	gracehttp.Serve(s)
}

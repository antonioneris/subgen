package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	terminalui "github.com/antonioneris/subgen/internal/ui"
)

const repoOwner = "antonioneris"
const repoName = "subgen"

func runUpdate(ctx context.Context, out, errOut io.Writer) error {
	styles := terminalui.New(out)

	fmt.Fprintln(out, styles.Title.Render("◆ SUBGEN · Atualização automática"))

	var release githubRelease
	errCheck := terminalui.RunTask(ctx, out, "Verificando versão mais recente...", 0, func(report func(int, int)) error {
		var err error
		release, err = fetchLatestRelease(ctx)
		return err
	})
	if errCheck != nil {
		return fmt.Errorf("falha ao verificar nova versão: %w", errCheck)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	currentVersion := strings.TrimPrefix(version, "v")

	if !isNewer(latestVersion, currentVersion) {
		fmt.Fprintf(out, "  %s\n", styles.Success.Render(fmt.Sprintf("✓ O subgen já está na versão mais recente (v%s).", version)))
		return nil
	}

	fmt.Fprintf(out, "  Nova versão encontrada: %s (sua versão: %s)\n", styles.Accent.Render("v"+latestVersion), styles.Muted.Render("v"+currentVersion))

	// Determinar os nomes dos arquivos
	osName := runtime.GOOS
	archName := runtime.GOARCH
	var archiveName string
	if osName == "windows" {
		archiveName = fmt.Sprintf("subgen_windows_%s.zip", archName)
	} else {
		archiveName = fmt.Sprintf("subgen_%s_%s.tar.gz", osName, archName)
	}

	var archiveURL, checksumsURL string
	for _, asset := range release.Assets {
		if asset.Name == archiveName {
			archiveURL = asset.DownloadURL
		} else if asset.Name == "checksums.txt" {
			checksumsURL = asset.DownloadURL
		}
	}

	if archiveURL == "" || checksumsURL == "" {
		return fmt.Errorf("arquivos para a arquitetura %s/%s não encontrados na release v%s", osName, archName, latestVersion)
	}

	tempDir, err := os.MkdirTemp("", "subgen-update-*")
	if err != nil {
		return fmt.Errorf("falha ao criar diretório temporário: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, archiveName)

	errDownload := terminalui.RunTask(ctx, out, "Baixando atualização...", 0, func(report func(int, int)) error {
		// Download do arquivo
		err := downloadFile(ctx, archiveURL, archivePath)
		if err != nil {
			return err
		}
		return nil
	})
	if errDownload != nil {
		return fmt.Errorf("falha no download: %w", errDownload)
	}

	// Baixar checksums.txt para validar
	checksumsContent, err := fetchURLBytes(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("falha ao baixar checksums: %w", err)
	}

	// Verificar o checksum do arquivo baixado
	expectedChecksum, err := parseChecksum(checksumsContent, archiveName)
	if err != nil {
		return fmt.Errorf("falha ao encontrar checksum do arquivo na release: %w", err)
	}

	actualChecksum, err := calculateSHA256(archivePath)
	if err != nil {
		return fmt.Errorf("falha ao calcular o checksum do arquivo baixado: %w", err)
	}

	if actualChecksum != expectedChecksum {
		return fmt.Errorf("checksum inválido (esperado: %s, obtido: %s); download recusado por segurança", expectedChecksum, actualChecksum)
	}

	// Extrair o executável
	binaryName := "subgen"
	if osName == "windows" {
		binaryName = "subgen.exe"
	}
	extractedBinaryPath := filepath.Join(tempDir, binaryName)

	if osName == "windows" {
		err = extractZip(archivePath, binaryName, extractedBinaryPath)
	} else {
		err = extractTarGz(archivePath, binaryName, extractedBinaryPath)
	}
	if err != nil {
		return fmt.Errorf("falha ao extrair executável: %w", err)
	}

	// Substituir o executável atual
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("falha ao identificar o caminho do executável atual: %w", err)
	}

	// Pegar o diretório do executável atual
	exeDir := filepath.Dir(exePath)

	// Criar arquivo temporário no mesmo diretório para garantir o mesmo filesystem (evita erro de invalid cross-device link)
	tempNewExe, err := os.CreateTemp(exeDir, "subgen-new-*")
	if err != nil {
		// Se falhar com permissão negada, avisa o usuário para usar sudo/admin
		if os.IsPermission(err) {
			return fmt.Errorf("permissão negada ao tentar escrever em %s. Tente executar novamente usando 'sudo subgen update'", exeDir)
		}
		return fmt.Errorf("falha ao criar arquivo temporário em %s: %w", exeDir, err)
	}
	tempNewExeName := tempNewExe.Name()
	defer os.Remove(tempNewExeName) // se rename der certo, o temp file já se move e esse remove não faz nada ou ignora

	// Copiar conteúdo do binário extraído para o arquivo temporário
	srcFile, err := os.Open(extractedBinaryPath)
	if err != nil {
		tempNewExe.Close()
		return fmt.Errorf("falha ao ler novo executável: %w", err)
	}
	defer srcFile.Close()

	_, err = io.Copy(tempNewExe, srcFile)
	tempNewExe.Close() // fecha antes de renomear/definir permissões
	if err != nil {
		return fmt.Errorf("falha ao copiar novo executável: %w", err)
	}

	// Permissão de execução no arquivo temporário
	err = os.Chmod(tempNewExeName, 0755)
	if err != nil {
		return fmt.Errorf("falha ao configurar permissões do executável: %w", err)
	}

	// Renomear (substituir)
	if osName == "windows" {
		// No Windows não podemos substituir um executável em execução.
		// Mas podemos renomeá-lo e colocar o novo no lugar!
		oldExePath := exePath + ".old"
		_ = os.Remove(oldExePath) // apaga qualquer resíduo antigo

		err = os.Rename(exePath, oldExePath)
		if err != nil {
			return fmt.Errorf("falha ao renomear executável atual para %s: %w. Verifique se tem permissão de administrador", filepath.Base(oldExePath), err)
		}

		err = os.Rename(tempNewExeName, exePath)
		if err != nil {
			// Tenta desfazer a renomeação do executável atual para não quebrar a instalação
			_ = os.Rename(oldExePath, exePath)
			return fmt.Errorf("falha ao instalar novo executável: %w", err)
		}

		fmt.Fprintf(out, "  %s\n", styles.Success.Render(fmt.Sprintf("✓ Atualizado com sucesso para v%s!", latestVersion)))
		fmt.Fprintf(out, "  Nota: O executável antigo foi renomeado para %s e será apagado na próxima execução do subgen.\n", filepath.Base(oldExePath))
	} else {
		// No Unix, renomear por cima de um binário em execução funciona perfeitamente
		err = os.Rename(tempNewExeName, exePath)
		if err != nil {
			if os.IsPermission(err) {
				return fmt.Errorf("permissão negada ao tentar substituir o executável em %s. Tente executar com 'sudo subgen update'", exePath)
			}
			return fmt.Errorf("falha ao substituir executável atual: %w", err)
		}
		fmt.Fprintf(out, "  %s\n", styles.Success.Render(fmt.Sprintf("✓ Atualizado com sucesso para v%s!", latestVersion)))
	}

	return nil
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("User-Agent", "subgen-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return githubRelease{}, errors.New("limite de requisições à API do GitHub excedido; tente novamente mais tarde")
	}
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("status HTTP inválido ao consultar GitHub API: %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}

	return release, nil
}

func isNewer(latest, current string) bool {
	lMaj, lMin, lPat := parseVersion(latest)
	cMaj, cMin, cPat := parseVersion(current)
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPat > cPat
}

func parseVersion(v string) (major, minor, patch int) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) > 0 {
		fmt.Sscanf(parts[0], "%d", &major)
	}
	if len(parts) > 1 {
		fmt.Sscanf(parts[1], "%d", &minor)
	}
	if len(parts) > 2 {
		fmt.Sscanf(parts[2], "%d", &patch)
	}
	return
}

func downloadFile(ctx context.Context, url string, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "subgen-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status HTTP %d ao baixar arquivo", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func fetchURLBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "subgen-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status HTTP %d ao baixar checksums", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func parseChecksum(checksumsContent []byte, filename string) (string, error) {
	lines := strings.Split(string(checksumsContent), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == filename {
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", fmt.Errorf("checksum para o arquivo %q não foi encontrado", filename)
}

func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractZip(zipPath, targetName, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	var targetFile *zip.File
	for _, f := range r.File {
		if filepath.Base(f.Name) == targetName {
			targetFile = f
			break
		}
	}

	if targetFile == nil {
		return fmt.Errorf("arquivo %q não encontrado dentro do zip", targetName)
	}

	rc, err := targetFile.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, targetFile.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

func extractTarGz(tarGzPath, targetName, destPath string) error {
	file, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if filepath.Base(header.Name) == targetName {
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			defer out.Close()

			_, err = io.Copy(out, tr)
			return err
		}
	}

	return fmt.Errorf("arquivo %q não encontrado dentro do tar.gz", targetName)
}

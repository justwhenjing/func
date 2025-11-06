package oci

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	slashpath "path"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

type goBuilder struct{}

func (b goBuilder) Base(customImage string) string {
	// 如果未定义，则返回空字符串，表示从头开始构建
	return customImage
}

func (b goBuilder) Configure(_ buildJob, _ v1.Platform, cf v1.ConfigFile) (v1.ConfigFile, error) {
	// 二进制文件放入 /func 目录中,直接执行
	cf.Config.Cmd = []string{"/func/f"}
	cf.Config.Env = append(cf.Config.Env, "LISTEN_ADDRESS=[::]:8080")
	return cf, nil
}

func (b goBuilder) WriteShared(_ buildJob) ([]imageLayer, error) {
	return []imageLayer{}, nil // 没有共享依赖生成在构建时
}

// WritePlatform 创建平台特定层
// 使用交叉编译生成静态链接的二进制文件，并打包成tar文件
func (b goBuilder) WritePlatform(cfg buildJob, p v1.Platform) (layers []imageLayer, err error) {
	var desc v1.Descriptor
	var layer v1.Layer

	// 1) 交叉编译
	exe, err := goBuild(cfg, p)
	if err != nil {
		return
	}

	// 2) 打包可执行文件
	target := filepath.Join(cfg.buildDir(), fmt.Sprintf("execlayer.%v.%v.tar.gz", p.OS, p.Architecture))
	if err = goExeTarball(exe, target, cfg.verbose); err != nil {
		return
	}

	// 3) 转换为OCI层
	if layer, err = tarball.LayerFromFile(target); err != nil {
		return
	}

	// Descriptor
	if desc, err = newDescriptor(layer); err != nil {
		return
	}
	desc.Platform = &p

	// Blob
	blob := filepath.Join(cfg.blobsDir(), desc.Digest.Hex)
	if cfg.verbose {
		fmt.Printf("mv %v %v\n", rel(cfg.buildDir(), target), rel(cfg.buildDir(), blob))
	}
	err = os.Rename(target, blob)
	if err != nil {
		return nil, fmt.Errorf("cannot rename blob: %w", err)
	}

	// NOTE: base is intentionally blank indiciating it is to be built without
	// a base layer.
	return []imageLayer{{Descriptor: desc, Layer: layer}}, nil
}

func goBuild(cfg buildJob, p v1.Platform) (binPath string, err error) {
	gobin, args, outpath, err := goBuildCmd(p, cfg)
	if err != nil {
		return
	}
	envs := goBuildEnvs(p)
	if cfg.verbose {
		fmt.Printf("%v %v\n", gobin, strings.Join(args, " "))
	} else {
		fmt.Printf("   %v\n", filepath.Base(outpath))
	}

	// 执行go mod tidy
	cmd := exec.CommandContext(cfg.ctx, gobin, "mod", "tidy")
	cmd.Env = envs
	cmd.Dir = cfg.buildDir()
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("go mod tidy failed: %w", err)
	}

	// 执行go build
	cmd = exec.CommandContext(cfg.ctx, gobin, args...)
	cmd.Env = envs
	cmd.Dir = cfg.buildDir()
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("go build failed: %w", err)
	}

	return outpath, nil
}

func goBuildCmd(p v1.Platform, cfg buildJob) (gobin string, args []string, outpath string, err error) {
	// Use the binary specified FUNC_GO if defined
	gobin = os.Getenv("FUNC_GO") // TODO: move to main and plumb through
	if gobin == "" {
		gobin = "go"
	}

	// Build as ./func/builds/$PID/result/f.$OS.$Architecture
	name := fmt.Sprintf("f.%v.%v", p.OS, p.Architecture)
	if p.Variant != "" {
		name = name + "." + p.Variant
	}
	outpath = filepath.Join("result", name)
	args = []string{"build", "-o", outpath}
	// TODO 此处有问题(在buildDir下执行,使用result相对路径,但是结果路径需要增加buildDir前缀)
	return gobin, args, filepath.Join(cfg.buildDir(), outpath), nil
}

func goBuildEnvs(p v1.Platform) (envs []string) {
	pegged := []string{
		"CGO_ENABLED=0",
		"GOOS=" + p.OS,
		"GOARCH=" + p.Architecture,
	}
	if p.Variant != "" && p.Architecture == "arm" {
		pegged = append(pegged, "GOARM="+strings.TrimPrefix(p.Variant, "v"))
	} else if p.Variant != "" && p.Architecture == "amd64" {
		pegged = append(pegged, "GOAMD64="+p.Variant)
	}

	isPegged := func(env string) bool {
		for _, v := range pegged {
			name := strings.Split(v, "=")[0]
			if strings.HasPrefix(env, name) {
				return true
			}
		}
		return false
	}

	envs = append(envs, pegged...)
	for _, env := range os.Environ() {
		if !isPegged(env) {
			envs = append(envs, env)
		}
	}
	return envs
}

func goExeTarball(source, target string, verbose bool) error {
	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	gw := gzip.NewWriter(targetFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	info, err := os.Stat(source)
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, info.Name())
	if err != nil {
		return err
	}
	header.Mode = (header.Mode & ^int64(fs.ModePerm)) | 0755

	header.Name = slashpath.Join("/func", "f")
	// TODO: should we set file timestamps to the build start time of cfg.t?
	// header.ModTime = timestampArgument

	if err = tw.WriteHeader(header); err != nil {
		return err
	}
	if verbose {
		fmt.Printf("→ %v \n", header.Name)
	}

	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()

	i, err := io.Copy(tw, file)
	if err != nil {
		return err
	}
	if verbose {
		fmt.Printf("  wrote %v bytes \n", i)
	}
	return nil
}

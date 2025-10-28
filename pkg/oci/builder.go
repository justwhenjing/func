package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	slashpath "path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/pkg/errors"

	fn "knative.dev/func/pkg/functions"
	"knative.dev/func/pkg/scaffolding"
)

const (
	DefaultUid = 1000
	DefaultGid = 1000
)

var defaultIgnored = []string{
	".git",
	".func",
	".funcignore",
	".gitignore",
}

// OCI 构建器支持的语言(根据key选择)
var builders = map[string]languageBuilder{
	"go":     goBuilder{},
	"python": pythonBuilder{},
}

// IsSupported is for UX.
func IsSupported(runtime string) bool {
	_, ok := builders[runtime]
	return ok
}

type imageLayer struct {
	Descriptor v1.Descriptor
	Layer      v1.Layer
}

type languageBuilder interface {
	// Base returns the base image (if any) to use.  Ideally this is a
	// multi-arch base image with a corresponding platform image for
	// each requested to be built.
	Base(customBase string) string

	// WriteShared layers (not platform-specific) which need to be genearted
	// on demand per language, such as shared dependencies.
	WriteShared(buildJob) ([]imageLayer, error)

	// WritePlatform layers which are specific to the
	WritePlatform(buildJob, v1.Platform) ([]imageLayer, error)

	// Configure a config with, for example, the entrypoint.
	// Called once per platform.
	Configure(buildJob, v1.Platform, v1.ConfigFile) (v1.ConfigFile, error)
}

type Builder struct {
	name    string // TODO: why is this used again?
	verbose bool   // log verbosely

	onDone func()          // 用于测试，完成通知
	impl   languageBuilder // 用于测试，构建实现的覆盖
}

// NewBuilder creates a builder instance.
func NewBuilder(name string, verbose bool) *Builder {
	return &Builder{name: name, verbose: verbose, onDone: func() {}}
}

// Build 构建一个OCI镜像的函数(类似docker打包)，包装在服务中，暴露接口作为网络服务。
// 平台是可选的，默认为fn.DefaultPlatforms
// "linux/amd64", "linux/arm64", "linux/arm/v7"
func (b *Builder) Build(ctx context.Context, f fn.Function, pp []fn.Platform) (err error) {
	// cmd中限制了只能使用默认的platform
	if len(pp) == 0 {
		pp = fn.DefaultPlatforms
	}

	// 1) 创建构建任务(根据语言选择构建器)
	job, err := newBuildJob(ctx, f, pp, b.verbose)
	if err != nil {
		return
	}
	if b.impl != nil {
		// 自定义构建器,用于测试
		job.languageBuilder = b.impl
	}

	// 2) 设置构建环境(创建目录)
	if err = setup(job); err != nil {
		return
	}
	defer cleanup(job)

	// 3) 生成脚手架代码
	if err = scaffold(job); err != nil {
		return
	}

	// 4) 容器化,输出镜像到 .func/builds
	if err = containerize(job); err != nil {
		return
	}

	// 5) 更新最后一次构建的链接 .func/builds/last
	if err = updateLastLink(job); err != nil {
		return
	}

	// 6) 通知可选的异步完成事件监听器（测试）
	b.onDone()

	// TODO: 目前通过无错误返回来传达构建完成状态的方式并不理想。系统需要依赖一个隐式约定：OCI镜像已经存在于当前进程的构建目录中。
	return
}

// setup 设置构建环境
func setup(job buildJob) (err error) {
	// 如果另一个构建正在进行，则失败
	if job.isActive() {
		return ErrBuildInProgress{job.buildDir()}
	}

	// 构建目录
	if _, err = os.Stat(job.buildDir()); !os.IsNotExist(err) {
		if job.verbose {
			fmt.Fprintf(os.Stderr, "rm -rf %v\n", job.buildDir())
		}
		if err = os.RemoveAll(job.buildDir()); err != nil {
			return
		}
	}
	if job.verbose {
		fmt.Fprintf(os.Stderr, "mkdir -p %v\n", job.buildDir())
	}
	if err = os.MkdirAll(job.buildDir(), 0774); err != nil {
		return
	}

	// PID链接目录
	if _, err = os.Stat(job.pidsDir()); os.IsNotExist(err) {
		if job.verbose {
			fmt.Fprintf(os.Stderr, "mkdir -p %v\n", job.pidsDir())
		}
		if err = os.MkdirAll(job.pidsDir(), 0774); err != nil {
			return
		}
	}

	// 链接到最后一次构建的尝试（这个）
	target := filepath.Join("..", "by-hash", job.hash)
	if job.verbose {
		fmt.Fprintf(os.Stderr, "ln -s %v %v\n", target, job.pidLink())
	}
	if err = os.Symlink(target, job.pidLink()); err != nil {
		return err
	}

	// 创建blob目录，层数据存储在这里(压缩和hash存储)
	if err := os.MkdirAll(job.blobsDir(), os.ModePerm); err != nil {
		return err
	}

	// 用于构建之间共享基础层的 Blob 缓存目录。
	// 注意：可能会将其转换为系统全局缓存（如果可用），位于
	// XDG_CONFIG_HOME/func/image-cache，当前实现作为后备方案：
	// TODO：虽然不太可能，但在活跃开发过程中，经过多轮基础层更改后，
	// 这个目录可能会变得难以管理。我们应该有某种方式来截断或
	// 缓解这种潜在的磁盘内存泄漏问题。
	if err := os.MkdirAll(job.cacheDir(), os.ModePerm); err != nil {
		return err
	}

	return
}

// cleanup 清理构建的文件系统工件
func cleanup(job buildJob) {
	// 清理孤立的构建链接
	dd, _ := os.ReadDir(job.pidsDir())
	for _, d := range dd {
		if processExists(d.Name()) {
			continue
		}
		dir := filepath.Join(job.pidsDir(), d.Name())
		if job.verbose {
			fmt.Fprintf(os.Stderr, "rm %v\n", dir)
		}
		_ = os.RemoveAll(dir)
	}

	// 删除构建文件目录，除非它们是：
	// 1. The build files from the last successful build
	// 2. 与pid链接相关联（当前正在进行）
	dd, _ = os.ReadDir(job.buildsDir())
	for _, d := range dd {
		dir := filepath.Join(job.buildsDir(), d.Name())
		if isLinkTo(job.lastLink(), dir) {
			continue
		}
		if job.isActive() {
			continue
		}
		if job.verbose {
			fmt.Fprintf(os.Stderr, "rm %v\n", dir)
		}
		_ = os.RemoveAll(dir)
	}
}

// scaffold 写出进程包装代码，当包含在最终容器中时，将实例化函数并将其作为服务暴露。
func scaffold(job buildJob) (err error) {
	// 提取嵌入的文件系统，其中包含给定运行时的 scaffolding
	repo, err := fn.NewRepository("", "")
	if err != nil {
		return
	}

	if job.verbose {
		fmt.Fprintf(os.Stderr, "Scaffolding to %v\n", job.buildDir())
	}

	return scaffolding.Write(
		job.buildDir(),       // desintation for scaffolding
		job.function.Root,    // source to be scaffolded
		job.function.Runtime, // scaffolding language to write
		job.function.Invoke, repo.FS())
}

// containerize 容器化整个服务，包括scaffolded函数、函数实现、基础镜像、数据层等。
func containerize(job buildJob) error {
	sharedLayers := []imageLayer{}

	if err := os.WriteFile(filepath.Join(job.ociDir(), "oci-layout"),
		[]byte(`{ "imageLayoutVersion": "1.0.0" }`), os.ModePerm); err != nil {
		return err
	}

	// 1) 创建共享层
	// - 数据层（源码）
	data, err := writeDataLayer(job)
	if err != nil {
		return err
	}
	sharedLayers = append(sharedLayers, data)

	// - 证书层
	certs, err := writeCertsLayer(job) // shared
	if err != nil {
		return err
	}
	sharedLayers = append(sharedLayers, certs)

	// - 语言特定共享层（如Python依赖）
	shared, err := job.languageBuilder.WriteShared(job)
	if err != nil {
		return err
	}
	sharedLayers = append(sharedLayers, shared...)

	// 2) 为每个平台创建镜像
	manifests := []v1.Descriptor{}
	for _, p := range job.platforms {
		// 创建平台特定层(根据语言来决定平台特定层的内容)
		platformSpecificLayers, err := job.languageBuilder.WritePlatform(job, p)
		if err != nil {
			return err
		}
		layers := append(sharedLayers, platformSpecificLayers...)

		// 拉取基础镜像(使用go-containerregistry)
		base, err := pullBase(job, p)
		if err != nil {
			return err
		}

		// 创建配置文件
		configFile, err := newConfigFile(job, p, base, layers)
		if err != nil {
			return err
		}
		configFile, err = job.languageBuilder.Configure(job, p, configFile)
		if err != nil {
			return err
		}

		// 写入配置
		config, err := writeConfig(job, configFile)
		if err != nil {
			return err
		}

		// 创建manifests清单
		manifest, err := writeManifest(job, p, base, config, layers)
		if err != nil {
			return err
		}
		manifests = append(manifests, manifest)
	}

	// 3) 创建镜像索引

	/*
		.func/builds/by-hash/{hash}/
		├── oci/
		│   ├── oci-layout          # OCI布局文件
		│   ├── index.json          # 镜像索引
		│   └── blobs/sha256/       # 所有层和配置的blob
		│       ├── {digest1}       # 数据层
		│       ├── {digest2}       # 证书层
		│       ├── {digest3}       # 语言层
		│       └── {digest4}       # 配置文件
		├── result/                 # 编译结果
		│   └── f.linux.amd64      # Go二进制文件
		└── service/               # 脚手架代码
		    └── main.py            # Python服务包装器
	*/

	return writeIndex(job, manifests)
}

// writeDataLayer 将源码打包成tar.gz(数据层)
func writeDataLayer(job buildJob) (layer imageLayer, err error) {
	// 创建根目录
	source := job.function.Root
	target := filepath.Join(job.buildDir(), "datalayer.tar.gz")

	// 创建源码压缩包，排除 .git, .func 等文件
	if err = newDataTarball(source, target, defaultIgnored, job.verbose); err != nil {
		return
	}

	// 转换为OCI层
	if layer.Layer, err = tarball.LayerFromFile(target); err != nil {
		return
	}

	// 生成描述符
	if layer.Descriptor, err = newDescriptor(layer.Layer); err != nil {
		return
	}

	// 移动到blobs目录
	blob := filepath.Join(job.blobsDir(), layer.Descriptor.Digest.Hex)
	if job.verbose {
		fmt.Fprintf(os.Stderr, "mv %v %v\n", rel(job.buildDir(), target), rel(job.buildDir(), blob))
	}
	err = os.Rename(target, blob)
	return
}

func newDataTarball(root, target string, ignored []string, verbose bool) error {
	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	gw := gzip.NewWriter(targetFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip files explicitly ignored
		for _, v := range ignored {
			if info.Name() == v {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		lnk := "" // if link, this will be used as the target
		if info.Mode()&fs.ModeSymlink != 0 {
			if lnk, err = validatedLinkTarget(root, path); err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(info, lnk)
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header.Name = slashpath.Join("/func", filepath.ToSlash(relPath))
		header.Uid = DefaultUid
		header.Gid = DefaultGid

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "→ %v \n", header.Name)
		}
		if !info.Mode().IsRegular() { //nothing more to do for non-regular
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tw, file)
		return err
	})
}

// validatedLinkTarget returns the target of a given link or an error if
// that target is either absolute or outside the given project root.
func validatedLinkTarget(root, path string) (tgt string, err error) {
	// tgt is the raw target of the link.
	// This path is either absolute or relative to the link's location.
	tgt, err = os.Readlink(path)
	if err != nil {
		return tgt, fmt.Errorf("cannot read link: %w", err)
	}

	// Absolute links will not be correct when copied into the runtime
	// container, because they are placed into path into '/func',
	if filepath.IsAbs(tgt) {
		return tgt, errors.New("project may not contain absolute links")
	}

	// Calculate the actual target of the link
	// (relative to the parent of the symlink)
	lnkTgt := filepath.Join(filepath.Dir(path), tgt)

	// Calculate the relative path from the function's root to
	// this actual target location
	relLnkTgt, err := filepath.Rel(root, lnkTgt)
	if err != nil {
		return
	}

	// Fail if this path is outside the function's root.
	if strings.HasPrefix(relLnkTgt, ".."+string(filepath.Separator)) || relLnkTgt == ".." {
		return tgt, errors.New("links must stay within project root")
	}
	return
}

// writeCertsLayer 创建证书层
func writeCertsLayer(job buildJob) (layer imageLayer, err error) {
	// 创建证书压缩包
	source := filepath.Join(job.buildDir(), "ca-certificates.crt")
	target := filepath.Join(job.buildDir(), "certslayer.tar.gz")

	// 创建根目录
	if err = newCertsTarball(source, target, job.verbose); err != nil {
		return
	}

	// 转换为OCI层
	if layer.Layer, err = tarball.LayerFromFile(target); err != nil {
		return
	}

	// 生成描述符
	if layer.Descriptor, err = newDescriptor(layer.Layer); err != nil {
		return
	}

	// 移动到blobs目录
	blob := filepath.Join(job.blobsDir(), layer.Descriptor.Digest.Hex)
	if job.verbose {
		fmt.Fprintf(os.Stderr, "mv %v %v\n", rel(job.buildDir(), target), rel(job.buildDir(), blob))
	}
	err = os.Rename(target, blob)
	return
}

func newCertsTarball(source, target string, verbose bool) error {
	targetFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer targetFile.Close()

	gw := gzip.NewWriter(targetFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// 将系统证书复制到容器中的标准位置
	paths := []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-certificates.crt",
	}

	fi, err := os.Stat(source)
	if err != nil {
		return err
	}

	// For each ssl certs path we want to create
	for _, path := range paths {
		// Create a header for it
		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		header.Name = path
		header.Uid = DefaultUid
		header.Gid = DefaultGid

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "→ %v \n", header.Name)
		}
		file, err := os.Open(source)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		if err != nil {
			return err
		}
	}

	return nil
}

// pullBase 拉取基础镜像
// 如果构建器指定了基础镜像，则返回给定平台的远程镜像的描述符。
// 如果这是第一次获取，则自动下载其层到本地缓存，并将其blob链接到最终的OCI镜像。
func pullBase(job buildJob, p v1.Platform) (image v1.Image, err error) {
	baseImage := job.function.Build.BaseImage
	if job.languageBuilder.Base(baseImage) == "" {
		return // 从头开始构建
	}

	// 1) 解析镜像引用
	ref, err := name.ParseReference(job.languageBuilder.Base(baseImage))
	if err != nil {
		return
	}

	// 2) 拉取远程镜像(依赖OCI的默认认证支持)
	// 读取docker的配置文件 ~/.docker/config.json
	desc, err := remote.Get(ref, remote.WithPlatform(p))
	if err != nil {
		return
	}

	// 3) 获取镜像描述符，直接或通过平台索引
	if image, err = desc.Image(); err != nil {
		return
	}

	// 4) 环境基础镜像层
	layers, err := image.Layers()
	if err != nil {
		return
	}
	for _, layer := range layers {
		if err = writeBaseLayer(job, layer); err != nil {
			return
		}
	}
	return
}

func writeBaseLayer(job buildJob, layer v1.Layer) (err error) {
	if err = ensureCached(job, layer); err != nil {
		return
	}

	digest, err := layer.Digest()
	if err != nil {
		return
	}

	sourcePath := filepath.Join(job.cacheDir(), digest.Hex)
	destPath := filepath.Join(job.blobsDir(), digest.Hex)

	// Check if already added
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		return nil // layer already in blobs.
	}

	// Add it to the image via hard link
	if err := os.Link(sourcePath, destPath); err != nil {
		return fmt.Errorf("creating hard link for layer %s: %w", digest, err)
	}

	return

	// TODO: fallback to copying eg if windows without perms?
}

func ensureCached(job buildJob, layer v1.Layer) (err error) {
	digest, err := layer.Digest()
	if err != nil {
		return
	}

	cachePath := filepath.Join(job.cacheDir(), digest.Hex)
	if _, err = os.Stat(cachePath); !os.IsNotExist(err) {
		if job.verbose {
			fmt.Fprintf(os.Stderr, "Using cached base layer: %v\n", digest.Hex)
		}
		return
	}

	reader, err := layer.Compressed()
	if err != nil {
		return
	}
	defer reader.Close()

	file, err := os.Create(cachePath)
	if err != nil {
		return
	}

	if _, err = io.Copy(file, reader); err != nil {
		return
	}
	if job.verbose {
		fmt.Fprintf(os.Stderr, "Caching base image layer: %v\n", digest.Hex)
	}
	return
}

func newConfigFile(job buildJob, p v1.Platform, base v1.Image, imageLayers []imageLayer) (cfg v1.ConfigFile, err error) {
	// 配置文件
	cfg = v1.ConfigFile{
		Created:      v1.Time{Time: job.start},
		Architecture: p.Architecture,
		OS:           p.OS,
		OSVersion:    p.OSVersion,
		Variant:      p.Variant,
		// OSFeatures:   p.OSFeatures, // TODO: need to update dep to get this
		Config: v1.Config{
			Env:          newConfigEnvs(job),
			Volumes:      newConfigVolumes(job),
			ExposedPorts: map[string]struct{}{"8080/tcp": {}},
			WorkingDir:   "/func/",
			StopSignal:   "SIGKILL",
			User:         fmt.Sprintf("%v:%v", DefaultUid, DefaultGid),
			// Labels
		},
		// TODO: Create a separate history entry for each layer built for
		// each language (EmptyLayer=false).
		History: []v1.History{
			{
				Author:     "func",
				Created:    v1.Time{Time: job.start},
				Comment:    "func host builder",
				EmptyLayer: true,
			},
		},
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []v1.Hash{},
		},
	}
	// Populate Layer DiffIDs
	for _, imageLayer := range imageLayers {
		diffID, err := imageLayer.Layer.DiffID()
		if err != nil {
			return cfg, err
		}
		cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, diffID)
	}

	// Base Images
	// Carry over settings from the base.
	if base != nil {
		// Fetch base's config file
		baseCfg, err := base.ConfigFile()
		if err != nil {
			return cfg, err
		}

		// Reuse the base's user if defined
		if baseCfg.Config.User != "" {
			cfg.Config.User = baseCfg.Config.User
		}

		// Prepend ENVs
		cfg.Config.Env = append(baseCfg.Config.Env, cfg.Config.Env...)

		// Prepend history
		cfg.History = append(baseCfg.History, cfg.History...)

		// Prepend diffIDs
		cfg.RootFS.DiffIDs = append(baseCfg.RootFS.DiffIDs, cfg.RootFS.DiffIDs...)
	}

	return cfg, nil
}

// newConfigEnvs returns the final set of environment variables to build into
// the container.  This consists of func-provided build metadata envs as well
// as any environment variables provided on the function itself.
func newConfigEnvs(job buildJob) []string {
	// TODO:  long-term, the correct architecture is to not read env vars
	// from deep within a package, but rather to expose the setting as a
	// variable and leave interacting with the environment to main.
	// This is a shortcut used by many packages, however, so it will work for
	// now.
	gitbin := os.Getenv("FUNC_GIT") // Use if provided
	if gitbin == "" {
		gitbin = "git" // default to looking on PATH
	}

	envs := []string{}

	// FUNC_CREATED
	// Formats container timestamp as RFC3339; a stricter version of the ISO 8601
	// format used by the container image manifest's 'Created' attribute.
	envs = append(envs, "FUNC_CREATED="+job.start.Format(time.RFC3339))

	// FUNC_VERSION
	// If source controlled, and if being built from a system with git, the
	// environment FUNC_VERSION will be populated.  Otherwise it will exist
	// (to indicate this logic was executed) but have an empty value.
	if job.verbose {
		fmt.Fprintf(os.Stderr, "cd %v && export FUNC_VERSION=$(%v describe --tags)\n", job.function.Root, gitbin)
	}
	cmd := exec.CommandContext(job.ctx, gitbin, "describe", "--tags")
	cmd.Dir = job.function.Root
	output, err := cmd.Output()
	if err != nil {
		if job.verbose {
			fmt.Fprintf(os.Stderr, "WARN: unable to determine function version. %v\n", err)
		}
		envs = append(envs, "FUNC_VERSION=")
	} else {
		envs = append(envs, "FUNC_VERSION="+strings.TrimSpace(string(output)))
	}

	// TODO: OTHERS?
	// Other metadata that may be useful. Perhaps:
	//   - func client version (func cli) used when building this file?
	//   - user/environment which triggered this build?
	//   - A reflection of the function itself?  Image, registry, etc. etc?

	// ENVs defined on the Function
	return append(envs, job.function.Run.Envs.Slice()...)
}

func newConfigVolumes(job buildJob) map[string]struct{} {
	volumes := make(map[string]struct{})
	for _, v := range job.function.Run.Volumes {
		if v.Path == nil {
			continue // TODO: remove pointers from Volume and Env struct members
		}
		volumes[*v.Path] = struct{}{}
	}
	return volumes
}

func writeConfig(job buildJob, configFile v1.ConfigFile) (configDesc v1.Descriptor, err error) {
	configDesc, err = writeAsJSONBlob(job, "config.json", configFile)
	configDesc.MediaType = types.OCIConfigJSON
	return
}

// writeManifest creates an image manifest for the given platform.
// The image consists of the shared data layer which is provided
func writeManifest(job buildJob, p v1.Platform, base v1.Image, configDesc v1.Descriptor, layers []imageLayer) (v1.Descriptor, error) {

	// the layers for the final manifest.
	layerDescs := []v1.Descriptor{}

	// If a base was provided, prepend it's layers.
	if base != nil { // base is a v1.Image
		baseManifest, err := base.Manifest()
		if err != nil {
			return v1.Descriptor{}, err
		}
		layerDescs = baseManifest.Layers
	}

	// Append our layers
	for _, layer := range layers {
		layerDescs = append(layerDescs, layer.Descriptor)
	}

	// The final manifest for this platform's image
	manifest := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		Config:        configDesc,
		Layers:        layerDescs,
	}

	// Write it to blobs
	manifestDesc, err := writeAsJSONBlob(
		job,
		fmt.Sprintf("manifest.%v.%v.json", p.OS, p.Architecture),
		manifest)
	manifestDesc.MediaType = types.OCIManifestSchema1
	manifestDesc.Platform = &p

	// returning the blob's descriptor for inclusion in the index
	return manifestDesc, err
}

func writeIndex(job buildJob, manifests []v1.Descriptor) (err error) {
	index := v1.IndexManifest{
		SchemaVersion: 2,
		MediaType:     types.OCIImageIndex,
		Manifests:     manifests,
	}

	filePath := filepath.Join(job.ociDir(), "index.json")
	file, err := os.Create(filePath)
	if err != nil {
		return
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(index)
	return
}

// -----------------------
// Build Job
// -----------------------
//
// A struct which gathers configuration together for a single build job
// and provides some calculated fields for a little syntactic sugar.

// buildJob contains various settings for a single build
type buildJob struct {
	ctx             context.Context // build context
	start           time.Time       // Timestamp for this build
	hash            string          // a fingerprint of the fs at start
	function        fn.Function     // Function being built
	platforms       []v1.Platform   // Platforms to build
	languageBuilder languageBuilder // build implementation
	verbose         bool
}

// newBuildJob creates a struct which contains information about the current
// build job and convenience accessors to eg pertinent directories.
func newBuildJob(ctx context.Context, f fn.Function, pp []fn.Platform, verbose bool) (buildJob, error) {
	job := buildJob{
		ctx:       ctx,
		start:     time.Now(),
		function:  f,
		platforms: toPlatforms(pp),
		verbose:   verbose,
	}

	// Calculate a hash of the Function filesystem at time of start.
	var err error
	if job.hash, _, err = fn.Fingerprint(job.function.Root); err != nil {
		return job, fmt.Errorf("error calculating fingerprint for build. %w", err)
	}

	// 根据语言选择构建器
	var ok bool
	if job.languageBuilder, ok = builders[f.Runtime]; !ok {
		return job, fmt.Errorf("%v functions are not yet supported by the host builder", f.Runtime)
	}
	return job, nil
}

// some convenience accessors

func (j buildJob) lastLink() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "last")
}
func (j buildJob) pidsDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-pid")
}
func (j buildJob) pidLink() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-pid", strconv.Itoa(os.Getpid()))
}
func (j buildJob) buildsDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-hash")
}
func (j buildJob) buildDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-hash", j.hash)
}
func (j buildJob) ociDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-hash", j.hash, "oci")
}
func (j buildJob) blobsDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "builds", "by-hash", j.hash, "oci", "blobs", "sha256")
}
func (j buildJob) cacheDir() string {
	return filepath.Join(j.function.Root, fn.RunDataDir, "blob-cache")
}

// isActive returns false if an active build for this Function is detected.
func (j buildJob) isActive() bool {
	dd, _ := os.ReadDir(j.pidsDir())
	for _, d := range dd {
		// for each link in PIDs dir
		// the build is active if a process exists of the same name
		// AND it is a link to this job's build directory.
		link := filepath.Join(j.pidsDir(), d.Name())
		if processExists(d.Name()) && isLinkTo(link, j.buildDir()) {
			return true
		}
	}
	return false
}

// -------------------------
// Helpers
// -------------------------

// rel is a simple prefix trim used exclusively for verbose debugging
// statements to print paths as relative to the current build directory
// rather than absolute. Returns the path relative to the current working
// build directory.  If it is not a subpath, the full path is returned
// unchanged.
func rel(base, path string) string {
	if strings.HasPrefix(path, base) {
		return "." + strings.TrimPrefix(path, base)
	}
	return path
}

// processExists returns true if the process with the given PID
// exists.
func processExists(pid string) bool {
	p, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	process, err := os.FindProcess(p)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// isLinkTo returns true if link is a link to target.
func isLinkTo(link, target string) bool {
	var err error
	if link, err = filepath.EvalSymlinks(link); err != nil {
		return false
	}
	if link, err = filepath.Abs(link); err != nil {
		return false
	}

	if target, err = filepath.EvalSymlinks(target); err != nil {
		return false
	}
	if target, err = filepath.Abs(target); err != nil {
		return false
	}

	return link == target
}

func updateLastLink(job buildJob) error {
	if job.verbose {
		fmt.Fprintf(os.Stderr, "ln -s %v %v\n", job.buildDir(), job.lastLink())
	}
	_ = os.RemoveAll(job.lastLink())
	rp, err := filepath.Rel(filepath.Dir(job.lastLink()), job.buildDir())
	if err != nil {
		return err
	}
	return os.Symlink(rp, job.lastLink())
}

// toPlatforms converts func's implementation-agnostic Platform struct
// into to the OCI builder's implementation-specific go-containerregistry v1
// palatform.
// Examples:
// {OS: "linux", Architecture: "amd64"},
// {OS: "linux", Architecture: "arm64"},
// {OS: "linux", Architecture: "arm", Variant: "v6"},
// {OS: "linux", Architecture: "arm", Variant: "v7"},
// {OS: "darwin", Architecture: "amd64"},
// {OS: "darwin", Architecture: "arm64"},
func toPlatforms(pp []fn.Platform) []v1.Platform {
	platforms := make([]v1.Platform, len(pp))
	for i, p := range pp {
		platforms[i] = v1.Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant}
	}
	return platforms
}

func newDescriptor(layer v1.Layer) (desc v1.Descriptor, err error) {
	size, err := layer.Size()
	if err != nil {
		return
	}
	digest, err := layer.Digest()
	if err != nil {
		return
	}
	return v1.Descriptor{
		MediaType: types.OCILayer,
		Size:      size,
		Digest:    digest,
	}, nil
}

// writeAsJSONBlob encodes the object a json, creates a blob from it, and returns
// a partially-complted descriptor with the hash and size populated.
func writeAsJSONBlob(job buildJob, tempName string, data any) (desc v1.Descriptor, err error) {
	filePath := filepath.Join(job.buildDir(), tempName)
	file, err := os.Create(filePath)
	if err != nil {
		return
	}
	defer file.Close()

	h := sha256.New()
	w := io.MultiWriter(file, h)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err = enc.Encode(data); err != nil {
		return
	}

	hash := v1.Hash{Algorithm: "sha256", Hex: hex.EncodeToString(h.Sum(nil))}

	fileInfo, err := file.Stat()
	if err != nil {
		return
	}
	size := fileInfo.Size()

	// move -> blobs
	blobPath := filepath.Join(job.blobsDir(), hash.Hex)
	if job.verbose {
		fmt.Fprintf(os.Stderr, "mv %v %v\n", rel(job.buildDir(), filePath), rel(job.buildDir(), blobPath))
	}
	// Need to close before rename
	if err = file.Close(); err != nil {
		return
	}
	if err = os.Rename(filePath, blobPath); err != nil {
		return
	}

	return v1.Descriptor{
		Digest: hash,
		Size:   size,
	}, nil
}

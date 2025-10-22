package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/ory/viper"
	"github.com/spf13/cobra"

	"knative.dev/func/pkg/builders"
	pack "knative.dev/func/pkg/builders/buildpacks"
	"knative.dev/func/pkg/builders/s2i"
	"knative.dev/func/pkg/config"
	fn "knative.dev/func/pkg/functions"
	"knative.dev/func/pkg/oci"
)

func NewBuildCmd(newClient ClientFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a function container",
		Long: `
NAME
	{{rootCmdUse}} build - Build a function container locally without deploying

SYNOPSIS
	{{rootCmdUse}} build [-r|--registry] [--builder] [--builder-image]
		         [--push] [--username] [--password] [--token]
	             [--platform] [-p|--path] [-c|--confirm] [-v|--verbose]
		         [--build-timestamp] [--registry-insecure]

DESCRIPTION

	Builds a function's container image and optionally pushes it to the
	configured container registry.

	By default building is handled automatically when deploying (see the deploy
	subcommand). However, sometimes it is useful to build a function container
	outside of this normal deployment process, for example for testing or during
	composition when integrating with other systems. Additionally, the container
	can be pushed to the configured registry using the --push option.

	When building a function for the first time, either a registry or explicit
	image name is required.  Subsequent builds will reuse these option values.

EXAMPLES

	o Build a function container using the given registry.
	  The full image name will be calculated using the registry and function name.
	  $ {{rootCmdUse}} build --registry registry.example.com/alice

	o Build a function container using an explicit image name, ignoring registry
	  and function name.
	  $ {{rootCmdUse}} build --image registry.example.com/alice/f:latest

	o Rebuild a function using prior values to determine container name.
	  $ {{rootCmdUse}} build

	o Build a function specifying the Source-to-Image (S2I) builder
	  $ {{rootCmdUse}} build --builder=s2i

	o Build a function specifying the Pack builder with a custom Buildpack
	  builder image.
	  $ {{rootCmdUse}} build --builder=pack --builder-image=cnbs/sample-builder:bionic

`,
		SuggestFor: []string{"biuld", "buidl", "built"},
		PreRunE: bindEnv("image", "path", "builder", "registry", "confirm",
			"push", "builder-image", "base-image", "platform", "verbose",
			"build-timestamp", "registry-insecure", "username", "password", "token"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, args, newClient)
		},
	}

	// Global Config
	cfg, err := config.NewDefault()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "error loading config at '%v'. %v\n", config.File(), err)
	}

	// Function Context
	f, _ := fn.NewFunction(effectivePath())
	if f.Initialized() {
		cfg = cfg.Apply(f) // defined values on f take precedence over cfg defaults
	}

	// 通用配置

	// 构建器, 见clientopts
	cmd.Flags().StringP("builder", "b", cfg.Builder,
		fmt.Sprintf("Builder to use when creating the function's container. Currently supported builders are %s. ($FUNC_BUILDER)",
			KnownBuilders()))
	// 镜像仓库地址+镜像仓库命名空间, 可以使用-r 或者 FUNC_REGISTRY 指定
	cmd.Flags().StringP("registry", "r", cfg.Registry,
		"Container registry + registry namespace. (ex 'ghcr.io/myuser').  The full image name is automatically determined using this along with function name. ($FUNC_REGISTRY)")
	// 跳过TLS证书验证,可以使用--registry-insecure 或者 FUNC_REGISTRY_INSECURE 指定
	cmd.Flags().Bool("registry-insecure", cfg.RegistryInsecure,
		"Skip TLS certificate verification when communicating in HTTPS with the registry ($FUNC_REGISTRY_INSECURE)")

	// 上下文配置
	// 上下文配置,会存放到 func.yaml 文件中,不会变成通用配置
	builderImage := f.Build.BuilderImages[f.Build.Builder]

	// 指定构建器镜像,用于分阶段构建,可以使用--builder-image 或者 FUNC_BUILDER_IMAGE 指定
	cmd.Flags().StringP("builder-image", "", builderImage,
		"Specify a custom builder image for use by the builder other than its default. ($FUNC_BUILDER_IMAGE)")
	// 指定基础镜像,可以使用--base-image 或者 FUNC_BASE_IMAGE 指定(只有host模式可以使用)
	cmd.Flags().StringP("base-image", "", f.Build.BaseImage,
		"Override the base image for your function (host builder only)")
	// 指定构建镜像名称,可以使用--image 或者 FUNC_IMAGE 指定(只有host模式可以使用)
	cmd.Flags().StringP("image", "i", f.Image,
		"Full image name in the form [registry]/[namespace]/[name]:[tag] (optional). This option takes precedence over --registry ($FUNC_IMAGE)")

	// 静态配置(不会存放于任何位置)

	// 推送镜像到镜像仓库,可以使用--push
	cmd.Flags().BoolP("push", "u", false,
		"Attempt to push the function image to the configured registry after being successfully built")
	// 指定平台,可以使用--platform linux/amd64 linux/arm64之类
	cmd.Flags().StringP("platform", "", "",
		"Optionally specify a target platform, for example \"linux/amd64\" when using the s2i build strategy")
	// 用于镜像仓库认证(用户+密码 或者 token)
	cmd.Flags().StringP("username", "", "", "Username to use when pushing to the registry.")
	cmd.Flags().StringP("password", "", "", "Password to use when pushing to the registry.")
	cmd.Flags().StringP("token", "", "", "Token to use when pushing to the registry.")
	// 构建时间
	cmd.Flags().BoolP("build-timestamp", "", false, "Use the actual time as the created time for the docker image. This is only useful for buildpacks builder.")

	// 暂时隐藏基础认证标志
	_ = cmd.Flags().MarkHidden("username")
	_ = cmd.Flags().MarkHidden("password")
	_ = cmd.Flags().MarkHidden("token")

	// Oft-shared flags:
	addConfirmFlag(cmd, cfg.Confirm)
	addPathFlag(cmd)
	addVerboseFlag(cmd, cfg.Verbose)

	// 补全
	if err := cmd.RegisterFlagCompletionFunc("builder", CompleteBuilderList); err != nil {
		fmt.Println("internal: error while calling RegisterFlagCompletionFunc: ", err)
	}
	if err := cmd.RegisterFlagCompletionFunc("builder-image", CompleteBuilderImageList); err != nil {
		fmt.Println("internal: error while calling RegisterFlagCompletionFunc: ", err)
	}

	return cmd
}

func runBuild(cmd *cobra.Command, _ []string, newClient ClientFactory) (err error) {
	var (
		cfg buildConfig
		f   fn.Function
	)

	// 收集配置
	if cfg, err = newBuildConfig().Prompt(); err != nil {
		return
	}

	// 验证配置
	if err = cfg.Validate(); err != nil {
		return
	}

	// 加载func
	if f, err = fn.NewFunction(cfg.Path); err != nil {
		return
	}
	if !f.Initialized() {
		return fn.NewErrNotInitialized(f.Root)
	}

	// 加载配置
	f = cfg.Configure(f)

	// 设置上下文
	cmd.SetContext(cfg.WithValues(cmd.Context()))

	// 创建client(目前主要是选择builder)
	clientOptions, err := cfg.clientOptions()
	if err != nil {
		return
	}
	client, done := newClient(ClientConfig{Verbose: cfg.Verbose}, clientOptions...)
	defer done()

	// 构建
	buildOptions, err := cfg.buildOptions()
	if err != nil {
		return
	}
	if f, err = client.Build(cmd.Context(), f, buildOptions...); err != nil {
		return
	}

	// 推送镜像
	if cfg.Push {
		if f, _, err = client.Push(cmd.Context(), f); err != nil {
			return
		}
	}

	// 更新func.yaml
	if err = f.Write(); err != nil {
		return
	}
	return f.Stamp()
}

// WithValues returns a context populated with values from the build config
// which are provided to the system via the context.
func (c buildConfig) WithValues(ctx context.Context) context.Context {
	// Push
	ctx = context.WithValue(ctx, fn.PushUsernameKey{}, c.Username)
	ctx = context.WithValue(ctx, fn.PushPasswordKey{}, c.Password)
	ctx = context.WithValue(ctx, fn.PushTokenKey{}, c.Token)
	return ctx
}

type buildConfig struct {
	// Globals (builder, confirm, registry, verbose)
	config.Global

	// BuilderImage is the image (name or mapping) to use for building.  Usually
	// set automatically.
	BuilderImage string

	// Image name in full, including registry, repo and tag (overrides
	// image name derivation based on registry and function name)
	Image string

	// BaseImage is an image to build a function upon (host builder only)
	// TODO: gauron99 -- make option to add a path to dockerfile ?
	BaseImage string

	// Path of the function implementation on local disk. Defaults to current
	// working directory of the process.
	Path string

	// Platform ofr resultant image (s2i builder only)
	Platform string

	// Push the resulting image to the registry after building.
	Push bool

	// Username when specifying optional basic auth.
	Username string

	// Password when using optional basic auth.  Should be provided along
	// with Username.
	Password string

	// Token when performing basic auth using a bearer token.  Should be
	// exclusive with Username and Password.
	Token string

	// Build with the current timestamp as the created time for docker image.
	// This is only useful for buildpacks builder.
	WithTimestamp bool
}

// newBuildConfig gathers options into a single build request.
func newBuildConfig() buildConfig {
	return buildConfig{
		Global: config.Global{
			Builder:          viper.GetString("builder"),
			Confirm:          viper.GetBool("confirm"),
			Registry:         registry(), // deferred defaulting
			Verbose:          viper.GetBool("verbose"),
			RegistryInsecure: viper.GetBool("registry-insecure"),
		},
		BuilderImage:  viper.GetString("builder-image"),
		BaseImage:     viper.GetString("base-image"),
		Image:         viper.GetString("image"),
		Path:          viper.GetString("path"),
		Platform:      viper.GetString("platform"),
		Push:          viper.GetBool("push"),
		Username:      viper.GetString("username"),
		Password:      viper.GetString("password"),
		Token:         viper.GetString("token"),
		WithTimestamp: viper.GetBool("build-timestamp"),
	}
}

// Configure the given function.  Updates a function struct with all
// configurable values.  Note that buildConfig already includes function's
// current values, as they were passed through via flag defaults, so overwriting
// is a noop.
func (c buildConfig) Configure(f fn.Function) fn.Function {
	f = c.Global.Configure(f)
	if f.Build.Builder != "" && c.BuilderImage != "" {
		f.Build.BuilderImages[f.Build.Builder] = c.BuilderImage
	}
	f.Image = c.Image
	f.Build.BaseImage = c.BaseImage
	// Path, Platform and Push are not part of a function's state.
	return f
}

// Prompt the user with value of config members, allowing for interactive changes.
// Skipped if not in an interactive terminal (non-TTY), or if --confirm false (agree to
// all prompts) was set (default).
func (c buildConfig) Prompt() (buildConfig, error) {
	if !interactiveTerminal() {
		return c, nil
	}

	// If there is no registry nor explicit image name defined, the
	// Registry prompt is shown whether or not we are in confirm mode.
	// Otherwise, it is only showin if in confirm mode
	// NOTE: the default in this latter situation will ignore the current function
	// value and will always use the value from the config (flag or env variable).
	// This is not strictly correct and will be fixed when Global Config: Function
	// Context is available (PR#1416)
	f, err := fn.NewFunction(c.Path)
	if err != nil {
		return c, err
	}
	if (f.Registry == "" && c.Registry == "" && c.Image == "") || c.Confirm {
		fmt.Println("A registry for function images is required. For example, 'docker.io/tigerteam'.")
		err := survey.AskOne(
			&survey.Input{Message: "Registry for function images:", Default: c.Registry},
			&c.Registry,
			survey.WithValidator(NewRegistryValidator(c.Path)))
		if err != nil {
			return c, fn.ErrRegistryRequired
		}
		fmt.Println("Note: building a function the first time will take longer than subsequent builds")
	}

	// Remainder of prompts are optional and only shown if in --confirm mode
	if !c.Confirm {
		return c, nil
	}

	// Image Name Override
	// Calculate a better image name message which shows the value of the final
	// image name as it will be calculated if an explicit image name is not used.

	qs := []*survey.Question{
		{
			Name: "image",
			Prompt: &survey.Input{
				Message: "Optionally specify an exact image name to use (e.g. quay.io/boson/node-sample:latest)",
			},
		},
		{
			Name: "path",
			Prompt: &survey.Input{
				Message: "Project path:",
				Default: c.Path,
			},
		},
	}
	//
	// TODO(lkingland): add confirmation prompts for other config members here
	//
	err = survey.Ask(qs, &c)
	return c, err
}

// Validate 校验配置
func (c buildConfig) Validate() (err error) {
	// Builder value must refer to a known builder short name
	if err = ValidateBuilder(c.Builder); err != nil {
		return
	}

	// Platform 只支持 S2I 构建器
	if c.Platform != "" && c.Builder != builders.S2I {
		err = errors.New("only S2I builds currently support specifying platform")
		return
	}

	// BaseImage 只支持 Host 构建器
	if c.BaseImage != "" && c.Builder != "host" {
		err = errors.New("only host builds support specifying the base image")
	}
	return
}

// clientOptions returns options suitable for instantiating a client based on
// the current state of the build config object.
// This will be unnecessary and refactored away when the host-based OCI
// builder and pusher are the default implementations and the Pack and S2I
// constructors simplified.
//
// TODO: Platform is currently only used by the S2I builder.  This should be
// a multi-valued argument which passes through to the "host" builder (which
// supports multi-arch/platform images), and throw an error if either trying
// to specify a platform for buildpacks, or trying to specify more than one
// for S2I.
//
// TODO: As a further optimization, it might be ideal to only build the
// image necessary for the target cluster, since the end product of  a function
// deployment is not the contiainer, but rather the running service.

// clientOptions 根据构建配置对象的当前状态返回适合实例化客户端的选项。
func (c buildConfig) clientOptions() ([]fn.Option, error) {
	o := []fn.Option{fn.WithRegistry(c.Registry)}
	switch c.Builder {
	case builders.Host:
		// host构建器,使用标准OCI构建器,支持go和py。
		t := newTransport(c.RegistryInsecure) // may provide a custom impl which proxies
		creds := newCredentialsProvider(config.Dir(), t)
		o = append(o,
			fn.WithBuilder(oci.NewBuilder(builders.Host, c.Verbose)),
			fn.WithPusher(oci.NewPusher(c.RegistryInsecure, false, c.Verbose,
				oci.WithCredentialsProvider(creds),
				oci.WithVerbose(c.Verbose))),
		)
	case builders.Pack:
		// pack构建器,使用Buildpacks构建器,支持nodejs,typescript,go,python,quarkus,rust,springboot,但是需要docker或者podman
		o = append(o,
			fn.WithBuilder(pack.NewBuilder(
				pack.WithName(builders.Pack),
				pack.WithTimestamp(c.WithTimestamp),
				pack.WithVerbose(c.Verbose))))
	case builders.S2I:
		// s2i构建器,使用S2I构建器,支持nodejs,typescript,go,python,quarkus,需要docker
		o = append(o,
			fn.WithBuilder(s2i.NewBuilder(
				s2i.WithName(builders.S2I),
				s2i.WithVerbose(c.Verbose))))
	default:
		return o, builders.ErrUnknownBuilder{Name: c.Builder, Known: KnownBuilders()}
	}
	return o, nil
}

// buildOptions 构建参数
func (c buildConfig) buildOptions() (oo []fn.BuildOption, err error) {
	oo = []fn.BuildOption{}

	// Platforms 可以升级为多值字段
	// 各个构建器实现需要负责在其不支持此功能时抛出错误：
	// Pack 构建器：不支持多平台（无）
	// S2I 构建器：支持单平台（一个）
	// Host 构建器：支持多平台（多个）
	if c.Platform != "" {
		parts := strings.Split(c.Platform, "/")
		if len(parts) != 2 {
			return oo, fmt.Errorf("the value for --patform must be in the form [OS]/[Architecture].  eg \"linux/amd64\"")
		}
		oo = append(oo, fn.BuildWithPlatforms([]fn.Platform{{OS: parts[0], Architecture: parts[1]}}))
	}

	return
}

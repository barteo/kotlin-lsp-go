// Gradle init script that writes a build's Kotlin/JVM modules as JSON for kcls, the language
// server on kc, without compiling anything:
//
//   ./gradlew -q --no-configuration-cache -I kcls-model.init.gradle.kts -Pkcls.out=model.json kclsModel
//
// Each KotlinCompile task (compileKotlin, compileTestKotlin, an Android module's
// compile<Variant>Kotlin, a multiplatform module's compileKotlinJvm) is a module: its sources, Java
// sources, class path, friend paths, module name, JDK, JVM target, explicit API mode, opt-ins and
// compiler plugins, as tools/gradle/use-kc.init.gradle.kts reads them to compile with kc. With
// -Pkcls.sources=true (the default) the script also resolves the sources jars of each project's
// compile class paths, for navigation into libraries.
//
// The tasks' types are not on an init script's class path, so the script reads them through
// reflection, and depends on the Kotlin Gradle plugin's getters (checked with 2.4.20).
if (gradle.parent == null) {
  fun isKotlinCompile(task: Task) =
    task.javaClass.name.removeSuffix("_Decorated") == "org.jetbrains.kotlin.gradle.tasks.KotlinCompile"
  fun call(target: Any, name: String): Any? =
    target.javaClass.methods.first { it.name == name && it.parameterCount == 0 }.invoke(target)
  fun callPrefixed(target: Any, prefix: String): Any? =
    target.javaClass.methods.firstOrNull { it.name.startsWith(prefix) && it.parameterCount == 0 }
      ?.invoke(target)

  fun json(v: Any?): String = when (v) {
    null -> "null"
    is Boolean, is Number -> v.toString()
    is Map<*, *> -> v.entries.joinToString(",", "{", "}") { json(it.key.toString()) + ":" + json(it.value) }
    is Iterable<*> -> v.joinToString(",", "[", "]") { json(it) }
    else -> buildString {
      append('"')
      for (c in v.toString()) when {
        c == '"' -> append("\\\"")
        c == '\\' -> append("\\\\")
        c < ' ' -> append("\\u%04x".format(c.code))
        else -> append(c)
      }
      append('"')
    }
  }

  // group:artifact, for a jar in Gradle's cache (files-2.1/group/artifact/version/hash/jar).
  fun coordinates(jar: File): String {
    val parts = jar.path.split(File.separatorChar)
    val i = parts.indexOf("files-2.1")
    return if (i >= 0 && parts.size > i + 2) "${parts[i + 1]}:${parts[i + 2]}" else jar.nameWithoutExtension
  }
  val knownPlugins = mapOf(
    "compose-compiler-plugin" to "compose",
    "serialization-compiler-plugin" to "serialization",
    "parcelize-compiler" to "parcelize",
    "dev.zacsweers.metro:compiler" to "metro",
  )
  val registrars = listOf("CompilerPluginRegistrar", "ComponentRegistrar")
    .map { "META-INF/services/org.jetbrains.kotlin.compiler.plugin.$it" }

  fun module(task: Task): Map<String, Any?> {
    val m = linkedMapOf<String, Any?>(
      "project" to task.project.path, "projectDir" to task.project.projectDir.path, "task" to task.name,
    )
    fun files(name: String) = runCatching {
      (call(task, name) as org.gradle.api.file.FileCollection).asFileTree.files
    }.getOrDefault(emptySet())
    fun option(name: String) = runCatching {
      (call(call(task, "getCompilerOptions")!!, name) as org.gradle.api.provider.Provider<*>).orNull
    }.getOrNull()
    m["sources"] = (files("getSources") + files("getJavaSources"))
      .filter { it.name.endsWith(".kt") || it.name.endsWith(".java") }
      .map { it.path }.distinct().sorted()
    m["classPath"] = runCatching {
      (call(task, "getLibraries") as org.gradle.api.file.FileCollection).files.map { it.path }
    }.getOrDefault(emptyList())
    m["friendPaths"] = runCatching {
      (call(task, "getFriendPaths") as org.gradle.api.file.FileCollection).files.map { it.path }
    }.getOrDefault(emptyList())
    m["output"] = runCatching {
      (call(task, "getDestinationDirectory") as org.gradle.api.file.DirectoryProperty).get().asFile.path
    }.getOrNull()
    m["moduleName"] = option("getModuleName") as String?
    m["noJdk"] = option("getNoJdk") == true
    m["jdkHome"] = runCatching {
      val toolchain = call(task, "getKotlinJavaToolchain")!!
      val java = callPrefixed(toolchain, "getJavaExecutable") as org.gradle.api.file.RegularFileProperty
      java.get().asFile.canonicalFile.parentFile.parentFile.path
    }.getOrNull()
    m["jvmTarget"] = runCatching { option("getJvmTarget")?.let { call(it, "getTarget") as String } }.getOrNull()
    val freeArgs = (option("getFreeCompilerArgs") as List<*>?).orEmpty().filterIsInstance<String>()
    m["explicitApi"] = freeArgs.lastOrNull { it.startsWith("-Xexplicit-api=") }?.substringAfter('=')
      ?: runCatching {
        val mode = (callPrefixed(task, "getExplicitApiMode") as org.gradle.api.provider.Provider<*>?)?.orNull
        mode?.toString()?.lowercase()?.takeIf { it == "strict" || it == "warning" }
      }.getOrNull()
    m["multiPlatform"] = "-Xmulti-platform" in freeArgs || runCatching {
      (callPrefixed(task, "getMultiPlatformEnabled") as org.gradle.api.provider.Provider<*>?)?.orNull == true
    }.getOrDefault(false)
    m["optIns"] = ((option("getOptIn") as List<*>?).orEmpty().filterIsInstance<String>() +
      freeArgs.filter { it.startsWith("-opt-in=") }.map { it.substringAfter('=') }).distinct()
    m["plugins"] = runCatching {
      (call(task, "getPluginClasspath") as org.gradle.api.file.FileCollection).files
        .filter { f ->
          f.isFile && runCatching {
            java.util.zip.ZipFile(f).use { z -> registrars.any { z.getEntry(it) != null } }
          }.getOrDefault(false)
        }
        .map { coordinates(it) }
        .map { c -> knownPlugins.entries.firstOrNull { c.contains(it.key) }?.value ?: c }
        .distinct()
    }.getOrDefault(emptyList())
    return m
  }

  // The sources jars of the jars on a project's compile class paths, from the repositories.
  fun sourceJars(project: Project, into: MutableMap<String, String>) {
    val configurations = project.configurations.filter {
      it.isCanBeResolved && it.name.endsWith("ompileClasspath")
    }
    for (configuration in configurations) runCatching {
      val artifacts = configuration.incoming.artifactView { lenient(true) }.artifacts.artifacts
      val byComponent = artifacts
        .filter { it.id.componentIdentifier is org.gradle.api.artifacts.component.ModuleComponentIdentifier }
        .groupBy { it.id.componentIdentifier }
      if (byComponent.isEmpty()) return@runCatching
      val result = project.dependencies.createArtifactResolutionQuery()
        .forComponents(byComponent.keys)
        .withArtifacts(org.gradle.jvm.JvmLibrary::class.java, org.gradle.language.base.artifact.SourcesArtifact::class.java)
        .execute()
      for (component in result.resolvedComponents) {
        val sources = component.getArtifacts(org.gradle.language.base.artifact.SourcesArtifact::class.java)
          .filterIsInstance<org.gradle.api.artifacts.result.ResolvedArtifactResult>()
          .firstOrNull()?.file ?: continue
        for (binary in byComponent[component.id].orEmpty()) into[binary.file.path] = sources.path
      }
    }
  }

  rootProject {
    tasks.register("kclsModel") {
      description = "Writes the Kotlin modules of the build as JSON for kcls, to the file -Dkcls.out names."
    }
  }
  gradle.taskGraph.whenReady {
    if (allTasks.none { it.name == "kclsModel" && it.project == it.project.rootProject }) return@whenReady
    fun property(name: String) = System.getProperty(name) ?: gradle.startParameter.projectProperties[name]
    val out = property("kcls.out") ?: throw GradleException("kclsModel: no -Dkcls.out or -Pkcls.out")
    val modules = mutableListOf<Map<String, Any?>>()
    val errors = mutableListOf<String>()
    val jars = sortedMapOf<String, String>()
    for (project in rootProject.allprojects) {
      val compiles = runCatching { project.tasks.matching { isKotlinCompile(it) }.toList() }
        .onFailure { errors += "${project.path}: ${it.message}" }
        .getOrDefault(emptyList())
      for (task in compiles) {
        runCatching { modules += module(task) }.onFailure { errors += "${task.path}: ${it.message}" }
      }
      if (compiles.isNotEmpty() && property("kcls.sources") != "false") sourceJars(project, jars)
    }
    File(out).writeText(json(mapOf(
      "gradleVersion" to gradle.gradleVersion,
      "modules" to modules,
      "sourceJars" to jars,
      "errors" to errors,
    )))
  }
}

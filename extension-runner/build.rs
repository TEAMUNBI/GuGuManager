fn main() {
    let protoc = protoc_bin_vendored::protoc_bin_path().expect("vendored protoc");
    std::env::set_var("PROTOC", protoc);
    prost_build::compile_protos(
        &["../api/proto/gugumanager/extension/runner/v1/runner.proto"],
        &["../api/proto"],
    )
        .expect("compile extension runner protocol");
    println!("cargo:rerun-if-changed=../api/proto/gugumanager/extension/runner/v1/runner.proto");
    println!("cargo:rerun-if-changed=wit/extension.wit");
}

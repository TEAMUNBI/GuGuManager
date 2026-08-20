wit_bindgen::generate!({
    path: "../../extension-runner/wit",
    world: "extension",
});

struct Example;

impl Guest for Example {
    fn run(input: Vec<u8>) -> Result<Vec<u8>, String> {
        gugumanager::extension::host::progress(10, "validating input");
        let mut output = b"gugumanager-wasi-v1:".to_vec();
        output.extend(input.into_iter().map(|byte| byte.to_ascii_uppercase()));
        gugumanager::extension::host::progress(100, "complete");
        Ok(output)
    }
}

export!(Example);

use std::io::{self, Read, Write};

pub const MAX_FRAME_BYTES: usize = 16 * 1024 * 1024;

pub fn read_frame(reader: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    let mut length = [0u8; 4];
    match reader.read_exact(&mut length) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(error) => return Err(error),
    }
    let length = u32::from_be_bytes(length) as usize;
    if length == 0 || length > MAX_FRAME_BYTES {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "invalid frame length"));
    }
    let mut payload = vec![0u8; length];
    reader.read_exact(&mut payload)?;
    Ok(Some(payload))
}
pub fn write_frame(writer: &mut impl Write, payload: &[u8]) -> io::Result<()> {
    if payload.is_empty() || payload.len() > MAX_FRAME_BYTES {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "invalid frame length"));
    }
    writer.write_all(&(payload.len() as u32).to_be_bytes())?;
    writer.write_all(payload)?;
    writer.flush()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip_frame() {
        let mut encoded = Vec::new();
        write_frame(&mut encoded, b"hello").unwrap();
        assert_eq!(read_frame(&mut encoded.as_slice()).unwrap().unwrap(), b"hello");
    }

    #[test]
    fn rejects_oversized_frame_before_allocating() {
        let encoded = ((MAX_FRAME_BYTES as u32) + 1).to_be_bytes();
        assert_eq!(read_frame(&mut encoded.as_slice()).unwrap_err().kind(), io::ErrorKind::InvalidData);
    }
}

import sqlite3
from datetime import datetime
from dataclasses import dataclass
from typing import Optional, List, Tuple
import os
import os.path
import requests
import json
import audio

MESSAGES_DB_PATH = os.path.join('/app', 'store', 'messages.db')
# MESSAGES_DB_PATH = "/home/ubuntu/docker/whatsapp-mcp/store/messages.db"
WHATSAPP_DB_PATH = os.path.join('/app', 'store', 'whatsapp.db')
# WHATSAPP_DB_PATH = "/home/ubuntu/docker/whatsapp-mcp/store/whatsapp.db"


# Use environment variable for bridge host, default to localhost for development
BRIDGE_HOST = os.getenv('BRIDGE_HOST', 'localhost')
WHATSAPP_API_BASE_URL = f"http://{BRIDGE_HOST}:8080/api"

@dataclass
class Message:
    timestamp: datetime
    sender: str
    content: str
    is_from_me: bool
    chat_jid: str
    id: str
    chat_name: Optional[str] = None
    media_type: Optional[str] = None

@dataclass
class Chat:
    jid: str
    name: Optional[str]
    last_message_time: Optional[datetime]
    last_message: Optional[str] = None
    last_sender: Optional[str] = None
    last_is_from_me: Optional[bool] = None

    @property
    def is_group(self) -> bool:
        """Determine if chat is a group based on JID pattern."""
        return self.jid.endswith("@g.us")

@dataclass
class Contact:
    phone_number: str
    name: Optional[str]
    jid: str
    first_name: Optional[str] = None
    full_name: Optional[str] = None
    push_name: Optional[str] = None
    business_name: Optional[str] = None
    nickname: Optional[str] = None  # User-defined nickname

@dataclass
class MessageContext:
    message: Message
    before: List[Message]
    after: List[Message]

def get_sender_name(sender_jid: str) -> str:
    """Get the best available name for a sender using both contact and chat data."""
    try:
        # First check for custom nickname
        nickname = get_contact_nickname(sender_jid)
        if nickname:
            return nickname
        
        # Try to get rich contact information from WhatsApp store
        whatsapp_conn = sqlite3.connect(WHATSAPP_DB_PATH)
        whatsapp_cursor = whatsapp_conn.cursor()
        
        # Look for contact in WhatsApp contacts
        whatsapp_cursor.execute("""
            SELECT first_name, full_name, push_name, business_name
            FROM whatsmeow_contacts
            WHERE their_jid = ?
            LIMIT 1
        """, (sender_jid,))
        
        contact_result = whatsapp_cursor.fetchone()
        whatsapp_conn.close()
        
        if contact_result:
            first_name, full_name, push_name, business_name = contact_result
            # Return the best available name
            return full_name or push_name or first_name or business_name or sender_jid
        
        # Fall back to chat database
        messages_conn = sqlite3.connect(MESSAGES_DB_PATH)
        messages_cursor = messages_conn.cursor()
        
        # First try matching by exact JID
        messages_cursor.execute("""
            SELECT name
            FROM chats
            WHERE jid = ?
            LIMIT 1
        """, (sender_jid,))
        
        result = messages_cursor.fetchone()
        
        # If no result, try looking for the number within JIDs
        if not result:
            # Extract the phone number part if it's a JID
            if '@' in sender_jid:
                phone_part = sender_jid.split('@')[0]
            else:
                phone_part = sender_jid
                
            messages_cursor.execute("""
                SELECT name
                FROM chats
                WHERE jid LIKE ?
                LIMIT 1
            """, (f"%{phone_part}%",))
            
            result = messages_cursor.fetchone()
        
        if result and result[0]:
            return result[0]
        else:
            return sender_jid
        
    except sqlite3.Error as e:
        print(f"Database error while getting sender name: {e}")
        return sender_jid
    finally:
        if 'messages_conn' in locals():
            messages_conn.close()

def format_message(message: Message, show_chat_info: bool = True) -> None:
    """Print a single message with consistent formatting."""
    output = ""
    
    if show_chat_info and message.chat_name:
        output += f"[{message.timestamp:%Y-%m-%d %H:%M:%S}] Chat: {message.chat_name} "
    else:
        output += f"[{message.timestamp:%Y-%m-%d %H:%M:%S}] "
        
    content_prefix = ""
    if hasattr(message, 'media_type') and message.media_type:
        content_prefix = f"[{message.media_type} - Message ID: {message.id} - Chat JID: {message.chat_jid}] "
    
    try:
        sender_name = get_sender_name(message.sender) if not message.is_from_me else "Me"
        output += f"From: {sender_name}: {content_prefix}{message.content}\n"
    except Exception as e:
        print(f"Error formatting message: {e}")
    return output

def format_messages_list(messages: List[Message], show_chat_info: bool = True) -> None:
    output = ""
    if not messages:
        output += "No messages to display."
        return output
    
    for message in messages:
        output += format_message(message, show_chat_info)
    return output

def list_messages(
    after: Optional[str] = None,
    before: Optional[str] = None,
    sender_phone_number: Optional[str] = None,
    chat_jid: Optional[str] = None,
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
    include_context: bool = True,
    context_before: int = 1,
    context_after: int = 1
) -> List[Message]:
    """Get messages matching the specified criteria with optional context."""
    print(f"Debug: Database path: {MESSAGES_DB_PATH}")
    print(f"Debug: Database exists: {os.path.exists(MESSAGES_DB_PATH)}")
    
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        # Debug: Check if tables exist
        cursor.execute("SELECT name FROM sqlite_master WHERE type='table';")
        tables = cursor.fetchall()
        print(f"Debug: Available tables: {tables}")
        
        # Debug: Check row counts
        try:
            cursor.execute("SELECT COUNT(*) FROM messages")
            msg_count = cursor.fetchone()[0]
            print(f"Debug: Total messages in database: {msg_count}")
        except Exception as e:
            print(f"Debug: Error counting messages: {e}")
        
        # Build base query
        query_parts = ["SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type FROM messages"]
        query_parts.append("JOIN chats ON messages.chat_jid = chats.jid")
        where_clauses = []
        params = []
        
        # Add filters
        if after:
            try:
                after = datetime.fromisoformat(after)
            except ValueError:
                raise ValueError(f"Invalid date format for 'after': {after}. Please use ISO-8601 format.")
            
            where_clauses.append("messages.timestamp > ?")
            params.append(after)

        if before:
            try:
                before = datetime.fromisoformat(before)
            except ValueError:
                raise ValueError(f"Invalid date format for 'before': {before}. Please use ISO-8601 format.")
            
            where_clauses.append("messages.timestamp < ?")
            params.append(before)

        if sender_phone_number:
            where_clauses.append("messages.sender = ?")
            params.append(sender_phone_number)
            
        if chat_jid:
            where_clauses.append("messages.chat_jid = ?")
            params.append(chat_jid)
            
        if query:
            where_clauses.append("LOWER(messages.content) LIKE LOWER(?)")
            params.append(f"%{query}%")
            
        if where_clauses:
            query_parts.append("WHERE " + " AND ".join(where_clauses))
            
        # Add pagination
        offset = page * limit
        query_parts.append("ORDER BY messages.timestamp DESC")
        query_parts.append("LIMIT ? OFFSET ?")
        params.extend([limit, offset])
        
        cursor.execute(" ".join(query_parts), tuple(params))
        messages = cursor.fetchall()
        
        result = []
        for msg in messages:
            message = Message(
                timestamp=datetime.fromisoformat(msg[0]),
                sender=msg[1],
                chat_name=msg[2],
                content=msg[3],
                is_from_me=msg[4],
                chat_jid=msg[5],
                id=msg[6],
                media_type=msg[7]
            )
            result.append(message)
            
        if include_context and result:
            # Add context for each message
            messages_with_context = []
            for msg in result:
                context = get_message_context(msg.id, context_before, context_after)
                messages_with_context.extend(context.before)
                messages_with_context.append(context.message)
                messages_with_context.extend(context.after)
            
            return format_messages_list(messages_with_context, show_chat_info=True)
            
        # Format and display messages without context
        return format_messages_list(result, show_chat_info=True)    
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if 'conn' in locals():
            conn.close()


def get_message_context(
    message_id: str,
    before: int = 5,
    after: int = 5
) -> MessageContext:
    """Get context around a specific message."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        # Get the target message first
        cursor.execute("""
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
            WHERE messages.id = ?
        """, (message_id,))
        msg_data = cursor.fetchone()
        
        if not msg_data:
            raise ValueError(f"Message with ID {message_id} not found")
            
        target_message = Message(
            timestamp=datetime.fromisoformat(msg_data[0]),
            sender=msg_data[1],
            chat_name=msg_data[2],
            content=msg_data[3],
            is_from_me=msg_data[4],
            chat_jid=msg_data[5],
            id=msg_data[6],
            media_type=msg_data[8]
        )
        
        # Get messages before
        cursor.execute("""
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
            WHERE messages.chat_jid = ? AND messages.timestamp < ?
            ORDER BY messages.timestamp DESC
            LIMIT ?
        """, (msg_data[7], msg_data[0], before))
        
        before_messages = []
        for msg in cursor.fetchall():
            before_messages.append(Message(
                timestamp=datetime.fromisoformat(msg[0]),
                sender=msg[1],
                chat_name=msg[2],
                content=msg[3],
                is_from_me=msg[4],
                chat_jid=msg[5],
                id=msg[6],
                media_type=msg[7]
            ))
        
        # Get messages after
        cursor.execute("""
            SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type
            FROM messages
            JOIN chats ON messages.chat_jid = chats.jid
            WHERE messages.chat_jid = ? AND messages.timestamp > ?
            ORDER BY messages.timestamp ASC
            LIMIT ?
        """, (msg_data[7], msg_data[0], after))
        
        after_messages = []
        for msg in cursor.fetchall():
            after_messages.append(Message(
                timestamp=datetime.fromisoformat(msg[0]),
                sender=msg[1],
                chat_name=msg[2],
                content=msg[3],
                is_from_me=msg[4],
                chat_jid=msg[5],
                id=msg[6],
                media_type=msg[7]
            ))
        
        return MessageContext(
            message=target_message,
            before=before_messages,
            after=after_messages
        )
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        raise
    finally:
        if 'conn' in locals():
            conn.close()


def list_chats(
    query: Optional[str] = None,
    limit: int = 20,
    page: int = 0,
    include_last_message: bool = True,
    sort_by: str = "last_active"
) -> List[Chat]:
    """Get chats matching the specified criteria."""
    print(f"Debug: Database path: {MESSAGES_DB_PATH}")
    print(f"Debug: Database exists: {os.path.exists(MESSAGES_DB_PATH)}")
    
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        # Debug: Check if tables exist
        cursor.execute("SELECT name FROM sqlite_master WHERE type='table';")
        tables = cursor.fetchall()
        print(f"Debug: Available tables: {tables}")
        
        # Debug: Check row counts
        try:
            cursor.execute("SELECT COUNT(*) FROM chats")
            chat_count = cursor.fetchone()[0]
            print(f"Debug: Total chats in database: {chat_count}")
        except Exception as e:
            print(f"Debug: Error counting chats: {e}")
        
        # Build base query
        query_parts = ["""
            SELECT 
                chats.jid,
                chats.name,
                chats.last_message_time,
                messages.content as last_message,
                messages.sender as last_sender,
                messages.is_from_me as last_is_from_me
            FROM chats
        """]
        
        if include_last_message:
            query_parts.append("""
                LEFT JOIN messages ON chats.jid = messages.chat_jid 
                AND chats.last_message_time = messages.timestamp
            """)
            
        where_clauses = []
        params = []
        
        if query:
            where_clauses.append("(LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)")
            params.extend([f"%{query}%", f"%{query}%"])
            
        if where_clauses:
            query_parts.append("WHERE " + " AND ".join(where_clauses))
            
        # Add sorting
        order_by = "chats.last_message_time DESC" if sort_by == "last_active" else "chats.name"
        query_parts.append(f"ORDER BY {order_by}")
        
        # Add pagination
        offset = (page ) * limit
        query_parts.append("LIMIT ? OFFSET ?")
        params.extend([limit, offset])
        
        cursor.execute(" ".join(query_parts), tuple(params))
        chats = cursor.fetchall()
        
        result = []
        for chat_data in chats:
            chat = Chat(
                jid=chat_data[0],
                name=chat_data[1],
                last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
                last_message=chat_data[3],
                last_sender=chat_data[4],
                last_is_from_me=chat_data[5]
            )
            result.append(chat)
            
        return result
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if 'conn' in locals():
            conn.close()


def search_contacts(query: str) -> List[Contact]:
    """Search contacts by name or phone number using both WhatsApp contacts and chat data."""
    try:
        # Connect to both databases
        whatsapp_conn = sqlite3.connect(WHATSAPP_DB_PATH)
        whatsapp_cursor = whatsapp_conn.cursor()
        
        # Split query into characters to support partial matching
        search_pattern = '%' + query + '%'
        
        # Query WhatsApp contacts database for rich contact information
        whatsapp_cursor.execute("""
            SELECT DISTINCT 
                their_jid,
                first_name,
                full_name,
                push_name,
                business_name
            FROM whatsmeow_contacts
            WHERE 
                (LOWER(COALESCE(first_name, '')) LIKE LOWER(?) OR 
                 LOWER(COALESCE(full_name, '')) LIKE LOWER(?) OR 
                 LOWER(COALESCE(push_name, '')) LIKE LOWER(?) OR 
                 LOWER(COALESCE(business_name, '')) LIKE LOWER(?) OR
                 LOWER(their_jid) LIKE LOWER(?))
                AND their_jid NOT LIKE '%@g.us'
            ORDER BY 
                CASE 
                    WHEN full_name IS NOT NULL AND full_name != '' THEN full_name
                    WHEN push_name IS NOT NULL AND push_name != '' THEN push_name
                    WHEN first_name IS NOT NULL AND first_name != '' THEN first_name
                    ELSE their_jid
                END
            LIMIT 50
        """, (search_pattern, search_pattern, search_pattern, search_pattern, search_pattern))
        whatsapp_contacts = whatsapp_cursor.fetchall()           
        result = []
        # If whatsapp_contacts is not empty, use only those
        for contact_data in whatsapp_contacts:
            jid = contact_data[0]
            phone_number = jid.split('@')[0] if '@' in jid else jid
            # Determine best display name
            first_name = contact_data[1]
            full_name = contact_data[2]
            push_name = contact_data[3]
            business_name = contact_data[4]
            display_name = full_name or push_name or first_name or business_name or phone_number
            contact = Contact(
                phone_number=phone_number,
                name=display_name,
                jid=jid,
                first_name=first_name,
                full_name=full_name,
                push_name=push_name,
                business_name=business_name
            )
            result.append(contact)
        return result
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if 'whatsapp_conn' in locals():
            whatsapp_conn.close()


def get_contact_chats(jid: str, limit: int = 20, page: int = 0) -> List[Chat]:
    """Get all chats involving the contact.
    
    Args:
        jid: The contact's JID to search for
        limit: Maximum number of chats to return (default 20)
        page: Page number for pagination (default 0)
    """
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("""
            SELECT DISTINCT
                c.jid,
                c.name,
                c.last_message_time,
                m.content as last_message,
                m.sender as last_sender,
                m.is_from_me as last_is_from_me
            FROM chats c
            JOIN messages m ON c.jid = m.chat_jid
            WHERE m.sender = ? OR c.jid = ?
            ORDER BY c.last_message_time DESC
            LIMIT ? OFFSET ?
        """, (jid, jid, limit, page * limit))
        
        chats = cursor.fetchall()
        
        result = []
        for chat_data in chats:
            chat = Chat(
                jid=chat_data[0],
                name=chat_data[1],
                last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
                last_message=chat_data[3],
                last_sender=chat_data[4],
                last_is_from_me=chat_data[5]
            )
            result.append(chat)
            
        return result
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if 'conn' in locals():
            conn.close()


def get_last_interaction(jid: str) -> str:
    """Get most recent message involving the contact."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("""
            SELECT 
                m.timestamp,
                m.sender,
                c.name,
                m.content,
                m.is_from_me,
                c.jid,
                m.id,
                m.media_type
            FROM messages m
            JOIN chats c ON m.chat_jid = c.jid
            WHERE m.sender = ? OR c.jid = ?
            ORDER BY m.timestamp DESC
            LIMIT 1
        """, (jid, jid))
        
        msg_data = cursor.fetchone()
        
        if not msg_data:
            return None
            
        message = Message(
            timestamp=datetime.fromisoformat(msg_data[0]),
            sender=msg_data[1],
            chat_name=msg_data[2],
            content=msg_data[3],
            is_from_me=msg_data[4],
            chat_jid=msg_data[5],
            id=msg_data[6],
            media_type=msg_data[7]
        )
        
        return format_message(message)
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if 'conn' in locals():
            conn.close()


def get_chat(chat_jid: str, include_last_message: bool = True) -> Optional[Chat]:
    """Get chat metadata by JID."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        query = """
            SELECT 
                c.jid,
                c.name,
                c.last_message_time,
                m.content as last_message,
                m.sender as last_sender,
                m.is_from_me as last_is_from_me
            FROM chats c
        """
        
        if include_last_message:
            query += """
                LEFT JOIN messages m ON c.jid = m.chat_jid 
                AND c.last_message_time = m.timestamp
            """
            
        query += " WHERE c.jid = ?"
        
        cursor.execute(query, (chat_jid,))
        chat_data = cursor.fetchone()
        
        if not chat_data:
            return None
            
        return Chat(
            jid=chat_data[0],
            name=chat_data[1],
            last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
            last_message=chat_data[3],
            last_sender=chat_data[4],
            last_is_from_me=chat_data[5]
        )
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if 'conn' in locals():
            conn.close()


def get_direct_chat_by_contact(sender_phone_number: str) -> Optional[Chat]:
    """Get chat metadata by sender phone number."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("""
            SELECT 
                c.jid,
                c.name,
                c.last_message_time,
                m.content as last_message,
                m.sender as last_sender,
                m.is_from_me as last_is_from_me
            FROM chats c
            LEFT JOIN messages m ON c.jid = m.chat_jid 
                AND c.last_message_time = m.timestamp
            WHERE c.jid LIKE ? AND c.jid NOT LIKE '%@g.us'
            LIMIT 1
        """, (f"%{sender_phone_number}%",))
        
        chat_data = cursor.fetchone()
        
        if not chat_data:
            return None
            
        return Chat(
            jid=chat_data[0],
            name=chat_data[1],
            last_message_time=datetime.fromisoformat(chat_data[2]) if chat_data[2] else None,
            last_message=chat_data[3],
            last_sender=chat_data[4],
            last_is_from_me=chat_data[5]
        )
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if 'conn' in locals():
            conn.close()

def send_message(recipient: str, message: str) -> Tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        
        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {
            "recipient": recipient,
            "message": message,
        }
        
        response = requests.post(url, json=payload)
        
        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"
            
    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"

def send_file(recipient: str, media_path: str) -> Tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        
        if not media_path:
            return False, "Media path must be provided"
        
        if not os.path.isfile(media_path):
            return False, f"Media file not found: {media_path}"
        
        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {
            "recipient": recipient,
            "media_path": media_path
        }
        
        response = requests.post(url, json=payload)
        
        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"
            
    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"

def send_audio_message(recipient: str, media_path: str) -> Tuple[bool, str]:
    try:
        # Validate input
        if not recipient:
            return False, "Recipient must be provided"
        
        if not media_path:
            return False, "Media path must be provided"
        
        if not os.path.isfile(media_path):
            return False, f"Media file not found: {media_path}"

        if not media_path.endswith(".ogg"):
            try:
                media_path = audio.convert_to_opus_ogg_temp(media_path)
            except Exception as e:
                return False, f"Error converting file to opus ogg. You likely need to install ffmpeg: {str(e)}"
        
        url = f"{WHATSAPP_API_BASE_URL}/send"
        payload = {
            "recipient": recipient,
            "media_path": media_path
        }
        
        response = requests.post(url, json=payload)
        
        # Check if the request was successful
        if response.status_code == 200:
            result = response.json()
            return result.get("success", False), result.get("message", "Unknown response")
        else:
            return False, f"Error: HTTP {response.status_code} - {response.text}"
            
    except requests.RequestException as e:
        return False, f"Request error: {str(e)}"
    except json.JSONDecodeError:
        return False, f"Error parsing response: {response.text}"
    except Exception as e:
        return False, f"Unexpected error: {str(e)}"

def download_media(message_id: str, chat_jid: str) -> Optional[str]:
    """Download media from a message and return the local file path.
    
    Args:
        message_id: The ID of the message containing the media
        chat_jid: The JID of the chat containing the message
    
    Returns:
        The local file path if download was successful, None otherwise
    """
    try:
        url = f"{WHATSAPP_API_BASE_URL}/download"
        payload = {
            "message_id": message_id,
            "chat_jid": chat_jid
        }
        
        response = requests.post(url, json=payload)
        
        if response.status_code == 200:
            result = response.json()
            if result.get("success", False):
                path = result.get("path")
                print(f"Media downloaded successfully: {path}")
                return path
            else:
                print(f"Download failed: {result.get('message', 'Unknown error')}")
                return None
        else:
            print(f"Error: HTTP {response.status_code} - {response.text}")
            return None
            
    except requests.RequestException as e:
        print(f"Request error: {str(e)}")
        return None
    except json.JSONDecodeError:
        print(f"Error parsing response: {response.text}")
        return None
    except Exception as e:
        print(f"Unexpected error: {str(e)}")
        return None

def get_contact_by_jid(jid: str) -> Optional[Contact]:
    """Get detailed contact information by JID."""
    try:
        # First try WhatsApp contacts database
        whatsapp_conn = sqlite3.connect(WHATSAPP_DB_PATH)
        whatsapp_cursor = whatsapp_conn.cursor()
        
        whatsapp_cursor.execute("""
            SELECT their_jid, first_name, full_name, push_name, business_name
            FROM whatsmeow_contacts
            WHERE their_jid = ?
            LIMIT 1
        """, (jid,))
        
        contact_data = whatsapp_cursor.fetchone()
        whatsapp_conn.close()
        
        # Get custom nickname
        nickname = get_contact_nickname(jid)
        
        if contact_data:
            phone_number = contact_data[0].split('@')[0] if '@' in contact_data[0] else contact_data[0]
            display_name = nickname or contact_data[2] or contact_data[3] or contact_data[1] or contact_data[4] or phone_number
            
            return Contact(
                phone_number=phone_number,
                name=display_name,
                jid=contact_data[0],
                first_name=contact_data[1],
                full_name=contact_data[2],
                push_name=contact_data[3],
                business_name=contact_data[4],
                nickname=nickname
            )
        
        # Fall back to chats database
        messages_conn = sqlite3.connect(MESSAGES_DB_PATH)
        messages_cursor = messages_conn.cursor()
        
        messages_cursor.execute("""
            SELECT jid, name
            FROM chats
            WHERE jid = ? AND jid NOT LIKE '%@g.us'
            LIMIT 1
        """, (jid,))
        
        chat_data = messages_cursor.fetchone()
        messages_conn.close()
        
        if chat_data:
            phone_number = chat_data[0].split('@')[0] if '@' in chat_data[0] else chat_data[0]
            display_name = nickname or chat_data[1] or phone_number
            
            return Contact(
                phone_number=phone_number,
                name=display_name,
                jid=chat_data[0],
                nickname=nickname
            )
        
        return None
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None


def get_contact_by_phone(phone_number: str) -> Optional[Contact]:
    """Get contact information by phone number."""
    try:
        # Try different JID formats
        possible_jids = [
            f"{phone_number}@s.whatsapp.net",
            f"{phone_number}@c.us",
            phone_number
        ]
        
        for jid in possible_jids:
            contact = get_contact_by_jid(jid)
            if contact:
                return contact
        
        # Try partial matching in chats
        messages_conn = sqlite3.connect(MESSAGES_DB_PATH)
        messages_cursor = messages_conn.cursor()
        
        messages_cursor.execute("""
            SELECT jid, name
            FROM chats
            WHERE jid LIKE ? AND jid NOT LIKE '%@g.us'
            LIMIT 1
        """, (f"%{phone_number}%",))
        
        chat_data = messages_cursor.fetchone()
        messages_conn.close()
        
        if chat_data:
            actual_phone = chat_data[0].split('@')[0] if '@' in chat_data[0] else chat_data[0]
            return Contact(
                phone_number=actual_phone,
                name=chat_data[1] or actual_phone,
                jid=chat_data[0]
            )
        
        return None
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None


def list_all_contacts(limit: int = 100) -> List[Contact]:
    """Get all contacts with their detailed information."""
    try:
        contacts = []
        
        # Get contacts from WhatsApp store
        whatsapp_conn = sqlite3.connect(WHATSAPP_DB_PATH)
        whatsapp_cursor = whatsapp_conn.cursor()
                
        whatsapp_cursor.execute(f"""
            SELECT their_jid, first_name, full_name, push_name, business_name
            FROM whatsmeow_contacts
            WHERE 1=1 AND their_jid NOT LIKE '%@g.us'
            ORDER BY 
                CASE 
                    WHEN full_name IS NOT NULL AND full_name != '' THEN full_name
                    WHEN push_name IS NOT NULL AND push_name != '' THEN push_name
                    WHEN first_name IS NOT NULL AND first_name != '' THEN first_name
                    ELSE their_jid
                END
            LIMIT ?
        """, (limit,))
        
        whatsapp_contacts = whatsapp_cursor.fetchall()
        whatsapp_conn.close()
        
        for contact_data in whatsapp_contacts:
            jid = contact_data[0]
            phone_number = jid.split('@')[0] if '@' in jid else jid
            
            first_name = contact_data[1]
            full_name = contact_data[2]
            push_name = contact_data[3]
            business_name = contact_data[4]
            
            display_name = full_name or push_name or first_name or business_name or phone_number
            
            contact = Contact(
                phone_number=phone_number,
                name=display_name,
                jid=jid,
                first_name=first_name,
                full_name=full_name,
                push_name=push_name,
                business_name=business_name
            )
            contacts.append(contact)
        
        return contacts
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []


def format_contact_info(contact: Contact) -> str:
    """Format contact information for display."""
    output = f"📱 {contact.name} ({contact.phone_number})\n"
    output += f"   JID: {contact.jid}\n"
    
    if contact.full_name and contact.full_name != contact.name:
        output += f"   Full Name: {contact.full_name}\n"
    
    if contact.first_name and contact.first_name != contact.name:
        output += f"   First Name: {contact.first_name}\n"
    
    if contact.push_name and contact.push_name != contact.name:
        output += f"   Display Name: {contact.push_name}\n"
    
    if contact.business_name:
        output += f"   Business: {contact.business_name}\n"
    
    if contact.nickname:
        output += f"   Nickname: {contact.nickname}\n"
    
    return output

def set_contact_nickname(jid: str, nickname: str) -> Tuple[bool, str]:
    """Set a custom nickname for a contact."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        # Insert or update nickname
        cursor.execute("""
            INSERT OR REPLACE INTO contact_nicknames (jid, nickname, updated_at)
            VALUES (?, ?, CURRENT_TIMESTAMP)
        """, (jid, nickname))
        
        conn.commit()
        return True, f"Nickname '{nickname}' set for contact {jid}"
        
    except sqlite3.Error as e:
        return False, f"Database error: {e}"
    finally:
        if 'conn' in locals():
            conn.close()


def get_contact_nickname(jid: str) -> Optional[str]:
    """Get a contact's custom nickname."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("""
            SELECT nickname
            FROM contact_nicknames
            WHERE jid = ?
        """, (jid,))
        
        result = cursor.fetchone()
        return result[0] if result else None
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return None
    finally:
        if 'conn' in locals():
            conn.close()


def remove_contact_nickname(jid: str) -> Tuple[bool, str]:
    """Remove a contact's custom nickname."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("DELETE FROM contact_nicknames WHERE jid = ?", (jid,))
        
        if cursor.rowcount > 0:
            conn.commit()
            return True, f"Nickname removed for contact {jid}"
        else:
            return False, f"No nickname found for contact {jid}"
        
    except sqlite3.Error as e:
        return False, f"Database error: {e}"
    finally:
        if 'conn' in locals():
            conn.close()


def list_contact_nicknames() -> List[Tuple[str, str]]:
    """List all custom contact nicknames."""
    try:
        conn = sqlite3.connect(MESSAGES_DB_PATH)
        cursor = conn.cursor()
        
        cursor.execute("""
            SELECT jid, nickname
            FROM contact_nicknames
            ORDER BY nickname
        """)
        
        return cursor.fetchall()
        
    except sqlite3.Error as e:
        print(f"Database error: {e}")
        return []
    finally:
        if 'conn' in locals():
            conn.close()
